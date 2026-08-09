package checkpoint

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/Icatme/pi-go/agent"
)

const (
	sha256DigestHexLength = sha256.Size * 2
	finalizationTimeout   = 5 * time.Second
)

// Runner adds durable, targeted approval state around an agent.Runner.
type Runner struct {
	runner            *agent.Runner
	store             Store
	definitionVersion string
	approvalPolicy    ApprovalPolicy
	agentName         string
}

// NewRunner validates config and snapshots the agent definition.
func NewRunner(config RunnerConfig) (*Runner, error) {
	if strings.TrimSpace(config.DefinitionVersion) == "" {
		return nil, fmt.Errorf("definition version is required")
	}
	if isNilStore(config.Store) {
		return nil, fmt.Errorf("checkpoint store is required")
	}
	if config.Definition.ToolResolver != nil {
		return nil, fmt.Errorf("checkpoint runner requires a static tool set; ToolResolver is not supported")
	}
	if config.Definition.PrepareNextTurn != nil {
		return nil, fmt.Errorf("checkpoint runner does not support PrepareNextTurn because runtime overrides are not durable")
	}
	runner, err := agent.NewRunner(config.Definition)
	if err != nil {
		return nil, err
	}
	return &Runner{
		runner:            runner,
		store:             config.Store,
		definitionVersion: config.DefinitionVersion,
		approvalPolicy:    config.ApprovalPolicy,
		agentName:         config.Definition.Name,
	}, nil
}

// NewCheckpointID returns a cryptographically random checkpoint ID.
func NewCheckpointID() (CheckpointID, error) {
	value, err := newRandomID()
	return CheckpointID(value), err
}

// Run creates a checkpoint before starting a new agent run.
func (r *Runner) Run(ctx context.Context, id CheckpointID, snapshot agent.AgentSnapshot, prompts []agent.Message) *RunStream {
	return r.start(ctx, func(runCtx context.Context, emit chan<- agent.AgentEvent) (Outcome, error) {
		return r.run(runCtx, id, snapshot, prompts, emit)
	})
}

// Resume applies targeted decisions and resumes an interrupted tool batch.
func (r *Runner) Resume(ctx context.Context, id CheckpointID, params ResumeParams) *RunStream {
	return r.start(ctx, func(runCtx context.Context, emit chan<- agent.AgentEvent) (Outcome, error) {
		return r.resume(runCtx, id, params, emit)
	})
}

func (r *Runner) start(ctx context.Context, operation func(context.Context, chan<- agent.AgentEvent) (Outcome, error)) *RunStream {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	stream := &RunStream{
		events: make(chan agent.AgentEvent, checkpointEventBufferSize),
		done:   make(chan struct{}),
		cancel: cancel,
	}
	go func() {
		defer cancel()
		outcome, err := operation(runCtx, stream.events)
		stream.resultMu.Lock()
		stream.outcome = cloneOutcome(outcome)
		stream.err = err
		stream.resultMu.Unlock()
		close(stream.events)
		close(stream.done)
	}()
	return stream
}

func (r *Runner) run(ctx context.Context, id CheckpointID, snapshot agent.AgentSnapshot, prompts []agent.Message, emit chan<- agent.AgentEvent) (Outcome, error) {
	if err := validateCheckpointID(id); err != nil {
		return Outcome{CheckpointID: id}, err
	}
	if err := contextError(ctx); err != nil {
		return Outcome{CheckpointID: id}, err
	}
	if len(snapshot.PendingToolCalls) != 0 || snapshot.PendingToolControl != nil {
		return Outcome{CheckpointID: id}, fmt.Errorf("%w: Run cannot start from a snapshot with pending tool calls", ErrInvalidCheckpoint)
	}
	normalizedSnapshot, err := normalizeSnapshot(snapshot)
	if err != nil {
		return Outcome{CheckpointID: id}, err
	}
	normalizedPrompts, err := normalizeMessages(prompts)
	if err != nil {
		return Outcome{CheckpointID: id}, err
	}

	envelope := newEnvelope(id, r.definitionVersion, normalizedSnapshot)
	envelope, err = r.save(ctx, id, 0, envelope)
	if err != nil {
		return Outcome{CheckpointID: id}, err
	}

	gate := newApprovalGate(r.approvalPolicy, nil, r.definitionVersion)
	coreStream := r.runner.RunWithHooks(ctx, envelope.Snapshot, normalizedPrompts, agent.LoopHooks{ToolGate: gate.evaluate})
	next, runErr, end, toolActivity := consumeCoreStream(coreStream, emit)
	return r.finishExecution(ctx, id, envelope, nil, next, gate, runErr, end, toolActivity, emit)
}

func (r *Runner) resume(ctx context.Context, id CheckpointID, params ResumeParams, emit chan<- agent.AgentEvent) (Outcome, error) {
	if err := validateCheckpointID(id); err != nil {
		return Outcome{CheckpointID: id}, err
	}
	if err := contextError(ctx); err != nil {
		return Outcome{CheckpointID: id}, err
	}
	record, err := r.store.Load(ctx, id)
	if err != nil {
		return Outcome{CheckpointID: id}, err
	}
	envelope, err := decodeStoredCheckpoint(id, record)
	if err != nil {
		return Outcome{CheckpointID: id}, err
	}
	if envelope.DefinitionVersion != r.definitionVersion {
		return outcomeFromEnvelope(id, envelope), fmt.Errorf("%w: stored %q, runner %q", ErrDefinitionVersionMismatch, envelope.DefinitionVersion, r.definitionVersion)
	}

	switch envelope.Status {
	case StatusRunning:
		return outcomeFromEnvelope(id, envelope), fmt.Errorf("%w: checkpoint %q", ErrCheckpointBusy, id)
	case StatusCompleted, StatusFailed, StatusIndeterminate:
		return outcomeFromEnvelope(id, envelope), fmt.Errorf("%w: checkpoint %q has status %q", ErrCheckpointNotResumable, id, envelope.Status)
	case StatusInterrupted:
	default:
		return Outcome{CheckpointID: id}, fmt.Errorf("%w: unsupported status %q", ErrInvalidCheckpoint, envelope.Status)
	}

	activeInterrupts := cloneStoredInterrupts(envelope.Interrupts)
	unresolved, err := applyResumeDecisions(&envelope, params)
	if err != nil {
		return outcomeFromEnvelope(id, envelope), err
	}
	if len(unresolved) > 0 {
		envelope.Interrupts = unresolved
		saved, saveErr := r.save(ctx, id, envelope.Revision, envelope)
		if saveErr != nil {
			return transientOutcomeFromEnvelope(id, envelope), saveErr
		}
		emitSyntheticLifecycle(emit, r.agentName, nil)
		return outcomeFromEnvelope(id, saved), nil
	}

	envelope.Status = StatusRunning
	envelope.Interrupts = nil
	running, err := r.save(ctx, id, envelope.Revision, envelope)
	if err != nil {
		return transientOutcomeFromEnvelope(id, envelope), err
	}

	gate := newApprovalGate(r.approvalPolicy, decisionsByDigest(running.Decisions), r.definitionVersion)
	coreStream := r.runner.ResumePendingToolCallsWithHooks(ctx, running.Snapshot, agent.LoopHooks{ToolGate: gate.evaluate})
	next, runErr, end, toolActivity := consumeCoreStream(coreStream, emit)
	return r.finishExecution(ctx, id, running, activeInterrupts, next, gate, runErr, end, toolActivity, emit)
}

func (r *Runner) finishExecution(ctx context.Context, id CheckpointID, running checkpointEnvelope, previousInterrupts []storedInterrupt, snapshot agent.AgentSnapshot, gate *approvalGate, runErr error, end *agent.AgentEvent, toolActivity bool, emit chan<- agent.AgentEvent) (Outcome, error) {
	var suspended *agent.ToolCallsSuspendedError
	if errors.As(runErr, &suspended) {
		approvals := gate.suspendedApprovals()
		if len(approvals) == 0 {
			return r.indeterminateAfterExecution(id, running, snapshot, fmt.Errorf("%w: core suspended without approval requests", ErrInvalidCheckpoint), end, emit)
		}
		interrupts, err := newStoredInterrupts(approvals)
		if err != nil {
			return r.indeterminateAfterExecution(id, running, snapshot, err, end, emit)
		}
		running.Status = StatusInterrupted
		running.Snapshot = snapshot
		running.Interrupts = interrupts
		if toolActivity {
			running.Decisions = nil
		}
		running.Error = ""
		saved, err := r.saveFinal(ctx, id, running.Revision, running)
		if err != nil {
			return r.indeterminateAfterExecution(id, running, snapshot, err, end, emit)
		}
		emitTerminalEvent(end, nil, emit)
		return outcomeFromEnvelope(id, saved), nil
	}
	if runErr != nil && len(previousInterrupts) > 0 {
		if toolActivity || agent.ValidatePendingToolState(snapshot) != nil || !samePendingBatch(running.Snapshot, snapshot) {
			return r.indeterminateAfterExecution(id, running, snapshot, runErr, end, emit)
		}
		interrupts, err := rotateStoredInterrupts(previousInterrupts)
		if err != nil {
			return r.indeterminateAfterExecution(id, running, snapshot, errors.Join(runErr, err), end, emit)
		}
		running.Status = StatusInterrupted
		running.Snapshot = snapshot
		running.Interrupts = interrupts
		running.Decisions = nil
		running.Error = ""
		saved, err := r.saveFinal(ctx, id, running.Revision, running)
		if err != nil {
			return r.indeterminateAfterExecution(id, running, snapshot, errors.Join(runErr, err), end, emit)
		}
		emitTerminalEvent(end, runErr, emit)
		return outcomeFromEnvelope(id, saved), runErr
	}

	running.Snapshot = snapshot
	running.Interrupts = nil
	if runErr != nil {
		running.Status = StatusFailed
		running.Error = runErr.Error()
	} else {
		running.Status = StatusCompleted
		running.Error = ""
	}
	saved, saveErr := r.saveFinal(ctx, id, running.Revision, running)
	if saveErr != nil {
		return r.indeterminateAfterExecution(id, running, snapshot, saveErr, end, emit)
	}
	emitTerminalEvent(end, runErr, emit)
	return outcomeFromEnvelope(id, saved), runErr
}

func (r *Runner) indeterminateAfterExecution(id CheckpointID, running checkpointEnvelope, snapshot agent.AgentSnapshot, cause error, end *agent.AgentEvent, emit chan<- agent.AgentEvent) (Outcome, error) {
	running.Status = StatusIndeterminate
	if normalized, err := normalizeSnapshot(snapshot); err == nil {
		running.Snapshot = normalized
	} else {
		cause = errors.Join(cause, err)
	}
	running.Interrupts = nil
	running.Error = cause.Error()
	resultErr := errors.Join(ErrOutcomeIndeterminate, cause)
	emitTerminalEvent(end, resultErr, emit)
	return transientOutcomeFromEnvelope(id, running), resultErr
}

func (r *Runner) save(ctx context.Context, id CheckpointID, expected Revision, envelope checkpointEnvelope) (checkpointEnvelope, error) {
	if envelope.CheckpointID != id {
		return checkpointEnvelope{}, fmt.Errorf("%w: envelope checkpoint id %q does not match store key %q", ErrInvalidCheckpoint, envelope.CheckpointID, id)
	}
	if expected == ^Revision(0) {
		return checkpointEnvelope{}, &RevisionConflictError{CheckpointID: id, Expected: expected, Actual: expected}
	}
	envelope.Revision = expected + 1
	payload, err := encodeEnvelope(envelope)
	if err != nil {
		return checkpointEnvelope{}, err
	}
	record, err := r.store.CompareAndSwap(ctx, id, expected, payload)
	if err != nil {
		return checkpointEnvelope{}, err
	}
	return decodeStoredCheckpoint(id, record)
}

func (r *Runner) saveFinal(ctx context.Context, id CheckpointID, expected Revision, envelope checkpointEnvelope) (checkpointEnvelope, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalizationTimeout)
	defer cancel()
	return r.save(persistCtx, id, expected, envelope)
}

func consumeCoreStream(stream *agent.RunStream, emit chan<- agent.AgentEvent) (agent.AgentSnapshot, error, *agent.AgentEvent, bool) {
	var end *agent.AgentEvent
	toolActivity := false
	for event := range stream.Events() {
		if event.Type == agent.EventAgentEnd {
			cloned := event
			end = &cloned
			continue
		}
		if event.Type == agent.EventToolExecutionStart || event.Type == agent.EventToolExecutionEnd {
			toolActivity = true
		}
		emit <- event
	}
	snapshot, err := stream.Wait()
	return snapshot, err, end, toolActivity
}

func emitTerminalEvent(end *agent.AgentEvent, err error, emit chan<- agent.AgentEvent) {
	if end == nil {
		event := agent.AgentEvent{Type: agent.EventAgentEnd, Timestamp: time.Now().UTC(), Err: err}
		emit <- event
		return
	}
	event := *end
	event.Err = err
	emit <- event
}

func emitSyntheticLifecycle(emit chan<- agent.AgentEvent, agentName string, err error) {
	runID, randomErr := newRandomID()
	if randomErr != nil {
		runID = fmt.Sprintf("checkpoint-%d", time.Now().UTC().UnixNano())
	}
	now := time.Now().UTC()
	emit <- agent.AgentEvent{
		Type:      agent.EventAgentStart,
		Timestamp: now,
		RunID:     runID,
		AgentName: agentName,
		Sequence:  1,
	}
	emit <- agent.AgentEvent{
		Type:      agent.EventAgentEnd,
		Timestamp: time.Now().UTC(),
		RunID:     runID,
		AgentName: agentName,
		Sequence:  2,
		Err:       err,
	}
}

func applyResumeDecisions(envelope *checkpointEnvelope, params ResumeParams) ([]storedInterrupt, error) {
	if len(params.Decisions) == 0 {
		return nil, fmt.Errorf("%w: at least one active interrupt decision is required", ErrInvalidResume)
	}
	active := make(map[InterruptID]storedInterrupt, len(envelope.Interrupts))
	for _, interrupt := range envelope.Interrupts {
		active[interrupt.Interrupt.ID] = interrupt
	}
	for id, decision := range params.Decisions {
		if _, ok := active[id]; !ok {
			return nil, fmt.Errorf("%w: interrupt %q is stale or unknown", ErrInvalidResume, id)
		}
		if !validDecisionAction(decision.Action) {
			return nil, fmt.Errorf("%w: interrupt %q has action %q", ErrInvalidResume, id, decision.Action)
		}
	}

	unresolved := make([]storedInterrupt, 0, len(envelope.Interrupts)-len(params.Decisions))
	for _, interrupt := range envelope.Interrupts {
		decision, decided := params.Decisions[interrupt.Interrupt.ID]
		if decided {
			envelope.Decisions = append(envelope.Decisions, storedDecision{Digest: interrupt.Digest, Decision: decision})
			continue
		}
		rotated, err := newInterruptID()
		if err != nil {
			return nil, err
		}
		interrupt.Interrupt.ID = rotated
		unresolved = append(unresolved, interrupt)
	}
	return unresolved, nil
}

func decisionsByDigest(decisions []storedDecision) map[string]ResumeDecision {
	result := make(map[string]ResumeDecision, len(decisions))
	for _, stored := range decisions {
		result[stored.Digest] = stored.Decision
	}
	return result
}

type pendingApproval struct {
	digest  string
	request ToolApprovalRequest
	message string
}

type approvalGate struct {
	policy            ApprovalPolicy
	decisions         map[string]ResumeDecision
	definitionVersion string

	mu        sync.Mutex
	order     []string
	suspended map[string]pendingApproval
}

func newApprovalGate(policy ApprovalPolicy, decisions map[string]ResumeDecision, definitionVersion string) *approvalGate {
	return &approvalGate{
		policy:            policy,
		decisions:         decisions,
		definitionVersion: definitionVersion,
		suspended:         make(map[string]pendingApproval),
	}
}

func (g *approvalGate) evaluate(ctx context.Context, input agent.BeforeToolCallContext) (agent.ToolGateResult, error) {
	approval, err := makePendingApproval(g.definitionVersion, input.ToolCall, input.Args)
	if err != nil {
		return agent.ToolGateResult{}, err
	}
	if decision, ok := g.takeDecision(approval.digest); ok {
		switch decision.Action {
		case DecisionActionApprove:
			return agent.ToolGateResult{Action: agent.ToolGateActionAllow}, nil
		case DecisionActionReject:
			reason := decision.Reason
			if reason == "" {
				reason = "tool execution was rejected"
			}
			return agent.ToolGateResult{Action: agent.ToolGateActionBlock, Reason: reason}, nil
		default:
			return agent.ToolGateResult{}, fmt.Errorf("%w: stored action %q", ErrInvalidCheckpoint, decision.Action)
		}
	}

	if g.policy == nil {
		return agent.ToolGateResult{Action: agent.ToolGateActionAllow}, nil
	}
	requirement, err := g.policy(ctx, cloneApprovalRequest(approval.request))
	if err != nil {
		return agent.ToolGateResult{}, err
	}
	if !requirement.Required {
		return agent.ToolGateResult{Action: agent.ToolGateActionAllow}, nil
	}
	approval.message = requirement.Message
	g.mu.Lock()
	if _, exists := g.suspended[approval.digest]; !exists {
		g.order = append(g.order, approval.digest)
		g.suspended[approval.digest] = approval
	}
	g.mu.Unlock()
	return agent.ToolGateResult{Action: agent.ToolGateActionSuspend, Reason: requirement.Message}, nil
}

func (g *approvalGate) takeDecision(digest string) (ResumeDecision, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	decision, ok := g.decisions[digest]
	if ok {
		delete(g.decisions, digest)
	}
	return decision, ok
}

func (g *approvalGate) suspendedApprovals() []pendingApproval {
	g.mu.Lock()
	defer g.mu.Unlock()
	approvals := make([]pendingApproval, 0, len(g.order))
	for _, digest := range g.order {
		approval := g.suspended[digest]
		approval.request = cloneApprovalRequest(approval.request)
		approvals = append(approvals, approval)
	}
	return approvals
}

func makePendingApproval(definitionVersion string, call agent.ToolCall, args any) (pendingApproval, error) {
	arguments, err := canonicalJSON(args)
	if err != nil {
		return pendingApproval{}, fmt.Errorf("%w: approval arguments are not durable: %v", ErrInvalidCheckpoint, err)
	}
	originalID := call.OriginalID
	if originalID == "" {
		originalID = call.ID
	}
	rawArguments, err := canonicalToolCallArguments(call.Arguments)
	if err != nil {
		return pendingApproval{}, fmt.Errorf("%w: raw tool arguments are not durable: %v", ErrInvalidCheckpoint, err)
	}
	binding := struct {
		Domain             string          `json:"domain"`
		DefinitionVersion  string          `json:"definition_version"`
		ToolCallID         string          `json:"tool_call_id"`
		OriginalToolCallID string          `json:"original_tool_call_id"`
		ToolName           string          `json:"tool_name"`
		RawArguments       json.RawMessage `json:"raw_arguments"`
		Arguments          json.RawMessage `json:"arguments"`
	}{
		Domain:             "pi-go.agent.tool-approval.v1",
		DefinitionVersion:  definitionVersion,
		ToolCallID:         call.ID,
		OriginalToolCallID: originalID,
		ToolName:           call.Name,
		RawArguments:       rawArguments,
		Arguments:          arguments,
	}
	payload, err := json.Marshal(binding)
	if err != nil {
		return pendingApproval{}, err
	}
	digest := sha256.Sum256(payload)
	return pendingApproval{
		digest: hex.EncodeToString(digest[:]),
		request: ToolApprovalRequest{
			ToolCallID:         call.ID,
			OriginalToolCallID: call.OriginalID,
			ToolName:           call.Name,
			Arguments:          append(json.RawMessage(nil), arguments...),
		},
	}, nil
}

func canonicalToolCallArguments(arguments json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(arguments)) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return canonicalJSON(arguments)
}

func canonicalJSON(value any) (json.RawMessage, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if err := rejectDuplicateJSONKeys(payload); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return nil, err
	}
	payload, err = json.Marshal(normalized)
	return json.RawMessage(payload), err
}

func newStoredInterrupts(approvals []pendingApproval) ([]storedInterrupt, error) {
	interrupts := make([]storedInterrupt, 0, len(approvals))
	for _, approval := range approvals {
		id, err := newInterruptID()
		if err != nil {
			return nil, err
		}
		message := approval.message
		if message == "" {
			message = fmt.Sprintf("approval required for tool %q", approval.request.ToolName)
		}
		interrupts = append(interrupts, storedInterrupt{
			Interrupt: Interrupt{
				ID:      id,
				Kind:    InterruptKindToolApproval,
				Message: message,
				Tool:    cloneApprovalRequest(approval.request),
			},
			Digest: approval.digest,
		})
	}
	return interrupts, nil
}

func cloneStoredInterrupts(interrupts []storedInterrupt) []storedInterrupt {
	if len(interrupts) == 0 {
		return nil
	}
	cloned := make([]storedInterrupt, len(interrupts))
	for index, interrupt := range interrupts {
		cloned[index] = interrupt
		cloned[index].Interrupt.Tool = cloneApprovalRequest(interrupt.Interrupt.Tool)
	}
	return cloned
}

func rotateStoredInterrupts(interrupts []storedInterrupt) ([]storedInterrupt, error) {
	rotated := cloneStoredInterrupts(interrupts)
	for index := range rotated {
		id, err := newInterruptID()
		if err != nil {
			return nil, err
		}
		rotated[index].Interrupt.ID = id
	}
	return rotated, nil
}

func samePendingBatch(left, right agent.AgentSnapshot) bool {
	if left.PendingToolControl == nil || right.PendingToolControl == nil || *left.PendingToolControl != *right.PendingToolControl {
		return false
	}
	if len(left.PendingToolCalls) != len(right.PendingToolCalls) {
		return false
	}
	for index := range left.PendingToolCalls {
		if left.PendingToolCalls[index] != right.PendingToolCalls[index] {
			return false
		}
	}
	return true
}

func newInterruptID() (InterruptID, error) {
	value, err := newRandomID()
	return InterruptID(value), err
}

func newRandomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate random id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func cloneApprovalRequest(request ToolApprovalRequest) ToolApprovalRequest {
	request.Arguments = append(json.RawMessage(nil), request.Arguments...)
	return request
}

func validateCheckpointID(id CheckpointID) error {
	if strings.TrimSpace(string(id)) == "" {
		return fmt.Errorf("checkpoint id is required")
	}
	return nil
}

func isNilStore(store Store) bool {
	if store == nil {
		return true
	}
	value := reflect.ValueOf(store)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
