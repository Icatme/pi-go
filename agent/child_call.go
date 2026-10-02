package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"unicode/utf8"
)

// ChildCaller is a capability issued only to an explicitly configured container.
// Its scope is fixed at parent entry and closes when the parent executor exits.
type ChildCaller interface {
	Call(context.Context, ToolCall) ToolCallOutcome
	Report() ChildCallReport
}

// ChildCallLimits may narrow the host defaults; a script cannot widen them.
type ChildCallLimits struct {
	MaxCalls         int
	MaxDetails       int
	MaxSummaryBytes  int
	MaxDetailBytes   int
	MaxArgumentBytes int
	MaxResultBytes   int
	MaxParallel      int
}

type ChildCallRecord struct {
	ToolCallID       string            `json:"tool_call_id"`
	ParentToolCallID string            `json:"parent_tool_call_id"`
	RunID            string            `json:"run_id,omitempty"`
	ToolName         string            `json:"tool_name"`
	ToolRevision     string            `json:"tool_revision,omitempty"`
	PolicyRevision   string            `json:"policy_revision,omitempty"`
	Execution        ToolExecutionInfo `json:"execution"`
	Failure          *ToolFailure      `json:"failure,omitempty"`
	Completed        bool              `json:"completed"`
	Err              error             `json:"-"`
}

type ChildCallDetail struct {
	ToolCallID string `json:"tool_call_id"`
	Summary    string `json:"summary,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Basic records remain complete even when optional detail retention is exhausted.
// Active records retain entered/unknown until the actual executor returns.
type ChildCallReport struct {
	ParentToolCallID string            `json:"parent_tool_call_id"`
	Calls            []ChildCallRecord `json:"calls"`
	Details          []ChildCallDetail `json:"details,omitempty"`
	DetailsTruncated bool              `json:"details_truncated,omitempty"`
	AdmissionStopped bool              `json:"admission_stopped,omitempty"`
	Closed           bool              `json:"closed"`
	Active           int               `json:"active"`
}

func cloneChildCallReport(report *ChildCallReport) *ChildCallReport {
	if report == nil {
		return nil
	}
	copy := *report
	copy.Details = append([]ChildCallDetail(nil), report.Details...)
	copy.Calls = make([]ChildCallRecord, len(report.Calls))
	for i, record := range report.Calls {
		copy.Calls[i] = record
		copy.Calls[i].Execution = *cloneToolExecutionInfo(&record.Execution)
		copy.Calls[i].Failure = cloneToolFailure(record.Failure)
	}
	return &copy
}

type childCallContextKey struct{}

type childCallContext struct {
	parentID string
	limits   ChildCallLimits
}

func childCallContextFrom(ctx context.Context) childCallContext {
	value, _ := ctx.Value(childCallContextKey{}).(childCallContext)
	return value
}

func childParentID(ctx context.Context) string {
	return childCallContextFrom(ctx).parentID
}

func argumentBudgetFailure(cause error) *ToolExecutionError {
	return &ToolExecutionError{Code: ToolFailureResource, Message: "child arguments exceed byte limit", Err: cause}
}

func checkArgumentBudget(value any, limit int) *ToolExecutionError {
	if limit > 0 && !valueFitsBudget(value, limit) {
		return argumentBudgetFailure(nil)
	}
	return nil
}

type childTicket struct {
	index   int
	ready   bool
	started bool
	start   chan struct{}
}

type childCallScope struct {
	ctx              context.Context
	cancel           context.CancelFunc
	engine           *Engine
	definition       AgentDefinition
	assistant        Message
	context          AgentContext
	tools            map[string]ToolDefinition
	gate             ToolGateHook
	parentID         string
	runID            string
	limits           ChildCallLimits
	emit             EventSink
	eventMu          sync.Mutex
	mu               sync.Mutex
	closed           bool
	stopped          bool
	active           int
	queue            []*childTicket
	records          []ChildCallRecord
	details          []ChildCallDetail
	detailBytes      int
	detailsTruncated bool
}

func normalizedChildLimits(limits ChildCallLimits) (ChildCallLimits, error) {
	fields := []*int{&limits.MaxCalls, &limits.MaxDetails, &limits.MaxSummaryBytes, &limits.MaxDetailBytes,
		&limits.MaxArgumentBytes, &limits.MaxResultBytes, &limits.MaxParallel}
	defaults := []int{1024, 256, 8192, 32768, 1 << 20, 16 << 20, 16}
	for i, field := range fields {
		if *field < 0 || *field > defaults[i] {
			return ChildCallLimits{}, fmt.Errorf("child limit %d must be between 0 and %d", i, defaults[i])
		}
		if *field == 0 {
			*field = defaults[i]
		}
	}
	return limits, nil
}

func validateChildTools(tool ToolDefinition) error {
	if tool.ChildTools == nil && tool.ResolveChildTools == nil {
		return nil
	}
	if tool.ChildTools != nil && tool.ResolveChildTools != nil {
		return fmt.Errorf("tool %q declares both static and resolved children", tool.Name)
	}
	if _, err := normalizedChildLimits(tool.ChildLimits); err != nil {
		return fmt.Errorf("tool %q: %w", tool.Name, err)
	}
	seen := make(map[string]struct{}, len(tool.ChildTools))
	for _, child := range tool.ChildTools {
		if child.Name == "" {
			return fmt.Errorf("tool %q has an unnamed child", tool.Name)
		}
		if _, exists := seen[child.Name]; exists {
			return fmt.Errorf("tool %q has duplicate child %q", tool.Name, child.Name)
		}
		seen[child.Name] = struct{}{}
		if child.ChildTools != nil || child.ResolveChildTools != nil {
			return fmt.Errorf("tool %q child %q is a container; deeper child calls are unsupported", tool.Name, child.Name)
		}
	}
	return validateToolDefinitions(tool.ChildTools)
}

func newChildCallScope(ctx context.Context, engine *Engine, definition AgentDefinition, assistant Message, parent preparedToolCall, emit EventSink) *childCallScope {
	scopeCtx, cancel := context.WithCancel(ctx)
	limits, _ := normalizedChildLimits(parent.tool.ChildLimits)
	tools := cloneTools(parent.tool.ChildTools)
	toolMap := make(map[string]ToolDefinition, len(tools))
	sequential := definition.ToolExecution == ToolExecutionSequential
	for _, tool := range tools {
		toolMap[tool.Name] = tool
		sequential = sequential || tool.ExecutionMode == ToolExecutionSequential
	}
	if sequential {
		limits.MaxParallel = 1
	}
	return &childCallScope{ctx: scopeCtx, cancel: cancel, engine: engine, definition: definition,
		assistant: cloneMessage(assistant), context: cloneAgentContext(parent.context), tools: toolMap,
		gate: parent.gate, parentID: parent.call.ID, runID: runnerRunIDFromContext(ctx), limits: limits, emit: emit}
}

func (s *childCallScope) Call(ctx context.Context, original ToolCall) ToolCallOutcome {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if s.closed || s.stopped {
		s.mu.Unlock()
		return rejectedChildOutcome(ToolCall{Name: boundedUTF8(original.Name, 1024)}, errors.New("child call scope is closed or admission stopped"), ToolFailureResource, s.limits.MaxResultBytes)
	}
	if len(s.records) >= s.limits.MaxCalls {
		s.stopped = true
		s.mu.Unlock()
		return rejectedChildOutcome(ToolCall{Name: boundedUTF8(original.Name, 1024)}, errors.New("child call budget exhausted"), ToolFailureResource, s.limits.MaxResultBytes)
	}
	index := len(s.records)
	id := fmt.Sprintf("%s/%d", s.parentID, index+1)
	name := boundedUTF8(original.Name, 1024)
	toolRevision := s.tools[name].Revision
	s.records = append(s.records, ChildCallRecord{ToolCallID: id, ParentToolCallID: s.parentID, RunID: s.runID,
		ToolName: name, ToolRevision: toolRevision, PolicyRevision: s.definition.PolicyRevision,
		Execution: ToolExecutionInfo{Local: ToolLocalNotStarted, Remote: ToolRemoteNotDispatched}})
	ticket := &childTicket{index: index, start: make(chan struct{})}
	s.queue = append(s.queue, ticket)
	s.mu.Unlock()
	call := ToolCall{ID: id, Name: name}
	if len(original.Name) > 1024 {
		return s.reject(ticket, call, errors.New("child tool name exceeds byte limit"), ToolFailureArgumentInvalid)
	}
	if len(original.Arguments) > s.limits.MaxArgumentBytes || !valueFitsBudget(original.ParsedArgs, s.limits.MaxArgumentBytes) {
		return s.reject(ticket, call, errors.New("child arguments exceed byte limit"), ToolFailureResource)
	}
	if err := checkArgumentStrings(original.ParsedArgs); err != nil {
		return s.reject(ticket, call, err, ToolFailureArgumentInvalid)
	}
	call.Arguments = append(call.Arguments, original.Arguments...)
	call.ParsedArgs = cloneStringAnyMap(original.ParsedArgs)
	callCtx, cancel := context.WithCancel(s.ctx)
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	defer cancel()
	callCtx = context.WithValue(callCtx, childCallContextKey{}, childCallContext{parentID: s.parentID, limits: s.limits})
	if err := ctx.Err(); err != nil {
		return s.reject(ticket, call, err, ToolFailureCanceled)
	}
	prepared, suspend, err := s.engine.prepareToolInvocation(callCtx, s.definition, s.assistant, s.context, s.tools, call, s.gate, nil)
	if err != nil {
		return s.reject(ticket, call, err, ToolFailureHook)
	}
	if suspend != nil {
		return s.reject(ticket, call, errors.New("nested tool suspension is unsupported"), ToolFailureNestedSuspendUnsupported)
	}
	if prepared.immediate {
		prepared.outcome.result = boundedErrorResult(prepared.outcome.result, prepared.outcome.err.Error(), s.limits.MaxResultBytes)
		outcome := publicToolOutcome(prepared.outcome)
		s.finish(ticket, outcome)
		s.emitEnd(outcome)
		return outcome
	}
	if err := s.acquire(callCtx, ticket); err != nil {
		return s.reject(ticket, call, err, ToolFailureCanceled)
	}
	defer s.release(ticket)
	prepared.child = true
	prepared.parentID = s.parentID
	prepared.resultLimit = s.limits.MaxResultBytes
	prepared.summaryLimit = s.limits.MaxSummaryBytes
	prepared.onEntered = func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.records[index].Execution = ToolExecutionInfo{Local: ToolLocalEntered, Remote: ToolRemoteUnknown}
	}
	s.emitChild(AgentEvent{Type: EventToolExecutionStart, ToolCallID: id, ToolName: name, ParentToolCallID: s.parentID,
		Execution: &ToolExecutionInfo{Local: ToolLocalNotStarted, Remote: ToolRemoteNotDispatched}})
	outcome, err := s.engine.executePreparedTool(callCtx, s.definition, s.assistant, prepared, s.emitChild)
	if err != nil {
		return s.reject(ticket, call, err, ToolFailureProtocol)
	}
	public := publicToolOutcome(outcome)
	s.finish(ticket, public)
	return public
}

func (s *childCallScope) acquire(ctx context.Context, ticket *childTicket) error {
	s.mu.Lock()
	ticket.ready = true
	s.pump()
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ticket.start:
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
}

func (s *childCallScope) pump() {
	for !s.closed && s.active < s.limits.MaxParallel && len(s.queue) > 0 {
		ticket := s.queue[0]
		if !ticket.ready {
			return
		}
		s.queue = s.queue[1:]
		ticket.started = true
		s.active++
		close(ticket.start)
	}
}

func (s *childCallScope) release(ticket *childTicket) {
	s.mu.Lock()
	if ticket.started {
		ticket.started = false
		s.active--
	}
	s.pump()
	s.mu.Unlock()
}

func (s *childCallScope) reject(ticket *childTicket, call ToolCall, err error, code ToolFailureCode) ToolCallOutcome {
	outcome := rejectedChildOutcome(call, err, code, s.limits.MaxResultBytes)
	s.finish(ticket, outcome)
	s.emitEnd(outcome)
	// A canceled queued call may already have received a scheduling permit.
	// No executor entered, so that permit can be released immediately.
	s.release(ticket)
	return outcome
}

func rejectedChildOutcome(call ToolCall, err error, code ToolFailureCode, resultLimit int) ToolCallOutcome {
	rejected := rejectedToolOutcome(call, err, code)
	rejected.result = boundedErrorResult(rejected.result, rejected.err.Error(), resultLimit)
	return publicToolOutcome(rejected)
}

func (s *childCallScope) finish(ticket *childTicket, outcome ToolCallOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, queued := range s.queue {
		if queued == ticket {
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			break
		}
	}
	record := &s.records[ticket.index]
	record.Completed = true
	record.Execution = *cloneToolExecutionInfo(&outcome.Execution)
	record.Failure = cloneToolFailure(outcome.Failure)
	record.Err = outcome.Err
	if len(s.details) >= s.limits.MaxDetails || s.detailBytes >= s.limits.MaxDetailBytes {
		s.detailsTruncated = true
	} else {
		remaining := min(s.limits.MaxSummaryBytes, s.limits.MaxDetailBytes-s.detailBytes)
		detail := ChildCallDetail{ToolCallID: outcome.ToolCall.ID, Summary: resultTextSummary(outcome.Result, remaining)}
		remaining -= len(detail.Summary)
		if outcome.Err != nil {
			message := resultTextSummary(outcome.Result, s.limits.MaxSummaryBytes)
			if message == "" && outcome.Failure != nil {
				message = string(outcome.Failure.Code)
			}
			detail.Error = boundedUTF8(boundedCodePoints(message, 500), remaining)
		}
		s.detailBytes += len(detail.Summary) + len(detail.Error)
		s.details = append(s.details, detail)
		if resultSummaryBytes(outcome.Result) > len(detail.Summary) {
			s.detailsTruncated = true
		}
	}
	s.pump()
}

func (s *childCallScope) emitEnd(outcome ToolCallOutcome) {
	s.emitChild(AgentEvent{Type: EventToolExecutionEnd, ToolCallID: outcome.ToolCall.ID, ToolName: outcome.ToolCall.Name,
		ParentToolCallID: s.parentID, IsError: outcome.IsError, Execution: cloneToolExecutionInfo(&outcome.Execution),
		Failure: cloneToolFailure(outcome.Failure), ToolResult: childResultSummary(outcome.Result, s.limits.MaxSummaryBytes)})
}

func (s *childCallScope) emitChild(event AgentEvent) {
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if !closed {
		emitEvent(s.emit, event)
	}
}

func (s *childCallScope) close() {
	s.cancel()
	s.eventMu.Lock()
	defer s.eventMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}

func (s *childCallScope) Report() ChildCallReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	report := ChildCallReport{ParentToolCallID: s.parentID, Details: append([]ChildCallDetail(nil), s.details...),
		DetailsTruncated: s.detailsTruncated, AdmissionStopped: s.stopped, Closed: s.closed, Active: s.active}
	report.Calls = make([]ChildCallRecord, len(s.records))
	for i, record := range s.records {
		report.Calls[i] = record
		report.Calls[i].Execution = *cloneToolExecutionInfo(&record.Execution)
		report.Calls[i].Failure = cloneToolFailure(record.Failure)
	}
	return report
}

func boundedUTF8(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return strings.Clone(text)
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return strings.Clone(text[:limit])
}

func boundedCodePoints(text string, limit int) string {
	count := 0
	for offset := range text {
		if count == limit {
			return strings.Clone(text[:offset])
		}
		count++
	}
	return strings.Clone(text)
}

func resultSummaryBytes(result ToolResult) int {
	total := 0
	for _, part := range result.Content {
		if part.Type == PartTypeText {
			total += len(part.Text)
		}
	}
	return total
}

func resultTextSummary(result ToolResult, limit int) string {
	var summary strings.Builder
	for _, part := range result.Content {
		if part.Type != PartTypeText || summary.Len() >= limit {
			continue
		}
		summary.WriteString(boundedUTF8(part.Text, limit-summary.Len()))
	}
	return summary.String()
}

func childResultSummary(result ToolResult, limit int) *ToolResult {
	return &ToolResult{Content: []Part{{Type: PartTypeText, Text: resultTextSummary(result, limit)}}, IsError: result.IsError,
		Execution: cloneToolExecutionInfo(result.Execution), Failure: cloneToolFailure(result.Failure)}
}

// Estimate before cloning. The traversal itself has bounded depth and work and
// accepts only data kinds, so cyclic or huge host values cannot allocate an
// unbounded event/bridge copy. Serialization and transport limits still apply.
func valueFitsBudget(value any, budget int) bool {
	_, fits := valueByteCost(value, budget)
	return fits
}

func valueByteCost(value any, budget int) (int, bool) {
	initialBudget := budget
	items := 0
	var visit func(reflect.Value, int) bool
	visit = func(value reflect.Value, depth int) bool {
		if !value.IsValid() {
			return true
		}
		// Original errors are retained by identity in Go-only fields; their
		// implementation graphs are neither serialized nor copied.
		if value.Type().Implements(reflect.TypeFor[error]()) {
			return true
		}
		items++
		if depth > 64 || items > 1<<18 || budget <= 0 {
			return false
		}
		budget -= 8
		switch value.Kind() {
		case reflect.Interface, reflect.Pointer:
			if value.IsNil() {
				return true
			}
			return visit(value.Elem(), depth+1)
		case reflect.String:
			budget -= value.Len()
			return budget >= 0
		case reflect.Slice, reflect.Array:
			if value.Kind() == reflect.Slice && value.IsNil() {
				return true
			}
			if value.Type().Elem().Kind() == reflect.Uint8 {
				budget -= value.Len()
				return budget >= 0
			}
			if value.Len() > budget/8 {
				return false
			}
			for i := 0; i < value.Len(); i++ {
				if !visit(value.Index(i), depth+1) {
					return false
				}
			}
			return true
		case reflect.Map:
			if value.Len() > budget/16 {
				return false
			}
			iter := value.MapRange()
			for iter.Next() {
				if !visit(iter.Key(), depth+1) || !visit(iter.Value(), depth+1) {
					return false
				}
			}
			return true
		case reflect.Struct:
			for i := 0; i < value.NumField(); i++ {
				field := value.Type().Field(i)
				if field.IsExported() && !visit(value.Field(i), depth+1) {
					return false
				}
			}
			return true
		case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
			return budget >= 0
		default:
			return false
		}
	}
	fits := visit(reflect.ValueOf(value), 0)
	return initialBudget - budget, fits
}
