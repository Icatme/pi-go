// Package turnloop provides a bounded, concurrent input loop around agent.Runner.
package turnloop

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/Icatme/pi-go/agent"
)

// Delivery selects when an admitted input may join an active runner invocation.
type Delivery string

const (
	// DeliveryNextRun waits for a new runner invocation and is never injected
	// into an invocation that is already active.
	DeliveryNextRun Delivery = "next-run"
	// DeliverySteering may be injected at a steering poll in an active invocation.
	DeliverySteering Delivery = "steering"
	// DeliveryFollowUp may be injected after steering is exhausted in an active invocation.
	DeliveryFollowUp Delivery = "follow-up"
)

// StopMode selects how a TurnLoop stops accepting and processing inputs.
type StopMode string

const (
	// StopGraceful finishes the active invocation and drains all admitted inputs.
	StopGraceful StopMode = "graceful"
	// StopImmediate cooperatively cancels the active invocation and returns all
	// inputs that have not yet been handed to the runner as unhandled.
	StopImmediate StopMode = "immediate"
)

var (
	// ErrAssistantFailure indicates an assistant-reported terminal failure with no underlying error cause.
	ErrAssistantFailure = errors.New("agent/turnloop: assistant failed")
	// ErrQueueFull indicates that the bounded waiting queue retained its existing
	// inputs and rejected the newest input.
	ErrQueueFull = errors.New("agent/turnloop: queue full")
	// ErrNotAccepting indicates that the loop has begun stopping or has stopped.
	ErrNotAccepting = errors.New("agent/turnloop: not accepting inputs")
	// ErrInvalidDelivery indicates that an input used an unknown delivery class.
	ErrInvalidDelivery = errors.New("agent/turnloop: invalid delivery")
	// ErrInvalidStopMode indicates that Stop received an unknown mode.
	ErrInvalidStopMode = errors.New("agent/turnloop: invalid stop mode")
	// ErrPendingSnapshot indicates that the initial snapshot contains a suspended
	// tool-call batch that requires an explicit resume operation.
	ErrPendingSnapshot = errors.New("agent/turnloop: initial snapshot has pending tool calls")
)

// Input is one message admitted to a TurnLoop.
type Input struct {
	Delivery Delivery
	Message  agent.Message
}

// Invocation owns one run's initial snapshot. session.RunBinding implements it.
// Implementations must use the supplied Runner and preserve its control hooks;
// they may add their own journal. Returning
// (nil, error) means no run was started and no hooks were called. A started run
// must return (stream, nil); its eventual failure belongs to stream.Wait.
// Implementations must honor ctx and must not retry or replay an invocation.
type Invocation interface {
	RunWithHooks(context.Context, *agent.Runner, []agent.Message, agent.LoopHooks) (*agent.RunStream, error)
}

// Config configures a TurnLoop.
type Config struct {
	// PrepareInvocation optionally restores host-owned state before each run,
	// outside the loop mutex and before input is taken from the queue. Initial
	// must be zero when set. A preparation failure stops without consuming input.
	// The callback must honor cancellation; unused bindings need no loop cleanup.
	PrepareInvocation func(context.Context) (Invocation, error)
	Definition        agent.AgentDefinition
	Initial           agent.AgentSnapshot
	QueueCapacity     int
	OnEvent           agent.EventSink
}

// Result is the terminal state of a TurnLoop, cloned with the same in-memory
// ownership semantics as agent.Runner.
type Result struct {
	Snapshot  agent.AgentSnapshot
	Unhandled []Input
	// StopMode is empty when a Runner or assistant failure, rather than a completed
	// stop request or parent cancellation, terminated the loop.
	StopMode StopMode
}

type stopSource uint8

const (
	stopSourceNone stopSource = iota
	stopSourceExplicit
	stopSourceParent
)

type queuedInput struct {
	Input
	receipt *Receipt
}

type invocationState struct {
	future              *receiptFuture
	completedTurns      int
	hookPolled          bool
	skipInitialSteering bool
}

// TurnLoop serially feeds admitted inputs through independent agent.Runner
// invocations. Its methods are safe for concurrent use.
type TurnLoop struct {
	mu sync.Mutex

	runner            *agent.Runner
	prepareInvocation func(context.Context) (Invocation, error)
	queueCapacity     int
	queue             []queuedInput
	steeringMode      agent.QueueMode
	followUpMode      agent.QueueMode
	maxTurns          int
	originalFinish    agent.FinishTurnHook
	onEvent           agent.EventSink
	active            *invocationState
	accepting         bool
	finished          bool
	requestedStop     StopMode
	stopSource        stopSource
	parentErr         error
	terminalResult    Result
	terminalErr       error

	runtimeCtx    context.Context
	runtimeCancel context.CancelCauseFunc
	parentCtx     context.Context
	wake          chan struct{}
	done          chan struct{}
}

// New validates config and starts an idle TurnLoop worker.
func New(ctx context.Context, config Config) (*TurnLoop, error) {
	if ctx == nil {
		return nil, errors.New("agent/turnloop: nil context")
	}
	if config.QueueCapacity <= 0 {
		return nil, fmt.Errorf("agent/turnloop: queue capacity must be positive: %d", config.QueueCapacity)
	}

	if config.PrepareInvocation != nil && !reflect.ValueOf(config.Initial).IsZero() {
		return nil, errors.New("agent/turnloop: Initial must be zero with PrepareInvocation")
	}
	definition, err := config.Definition.Validate()
	if err != nil {
		return nil, err
	}
	if err := validateQueueMode("steering", definition.SteeringMode); err != nil {
		return nil, err
	}
	if err := validateQueueMode("follow-up", definition.FollowUpMode); err != nil {
		return nil, err
	}
	if len(config.Initial.PendingToolCalls) != 0 || config.Initial.PendingToolControl != nil {
		return nil, ErrPendingSnapshot
	}

	runtimeCtx, runtimeCancel := context.WithCancelCause(ctx)
	loop := &TurnLoop{
		queueCapacity:     config.QueueCapacity,
		prepareInvocation: config.PrepareInvocation,
		steeringMode:      definition.SteeringMode,
		followUpMode:      definition.FollowUpMode,
		maxTurns:          definition.MaxTurns,
		originalFinish:    definition.FinishTurn,
		onEvent:           config.OnEvent,
		accepting:         true,
		runtimeCtx:        runtimeCtx,
		runtimeCancel:     runtimeCancel,
		parentCtx:         ctx,
		wake:              make(chan struct{}, 1),
		done:              make(chan struct{}),
	}
	definition.FinishTurn = loop.finishTurn
	runner, err := agent.NewRunner(definition)
	if err != nil {
		runtimeCancel(nil)
		return nil, err
	}
	loop.runner = runner
	initial := cloneValue(config.Initial)

	go loop.watchParent(ctx)
	go loop.run(initial)
	return loop, nil
}

// Push synchronously clones and admits input without waiting for queue space.
// Cloning follows agent.Runner's in-memory ownership semantics. Map keys and
// opaque values with mutable unexported fields must not be mutated after
// admission. When the queue is full, the newest input is rejected with
// ErrQueueFull.
func (l *TurnLoop) Push(input Input) error {
	return l.push(input, nil)
}

// PushTracked admits input exactly like Push and returns an optional settlement
// receipt. Rejected inputs return a nil receipt. Message.ID is not a deduplication key.
func (l *TurnLoop) PushTracked(input Input) (*Receipt, error) {
	receipt := &Receipt{ready: make(chan struct{})}
	if err := l.push(input, receipt); err != nil {
		return nil, err
	}
	return receipt, nil
}

func (l *TurnLoop) push(input Input, receipt *Receipt) error {
	if !validDelivery(input.Delivery) {
		return ErrInvalidDelivery
	}
	detached := cloneValue(input)

	l.mu.Lock()
	l.reconcileParentCancellationLocked()
	if !l.accepting {
		l.mu.Unlock()
		return ErrNotAccepting
	}
	if len(l.queue) >= l.queueCapacity {
		l.mu.Unlock()
		return ErrQueueFull
	}
	l.queue = append(l.queue, queuedInput{Input: detached, receipt: receipt})
	l.mu.Unlock()
	l.notify()
	return nil
}

// Stop atomically closes admission and requests graceful or immediate stop.
// Repeated calls are harmless; a graceful stop may be upgraded to immediate.
func (l *TurnLoop) Stop(mode StopMode) error {
	if !validStopMode(mode) {
		return ErrInvalidStopMode
	}

	cancel := false
	l.mu.Lock()
	l.reconcileParentCancellationLocked()
	if l.finished {
		l.mu.Unlock()
		return nil
	}
	switch l.requestedStop {
	case "":
		l.accepting = false
		l.requestedStop = mode
		l.stopSource = stopSourceExplicit
		cancel = mode == StopImmediate
	case StopGraceful:
		if mode == StopImmediate {
			l.requestedStop = StopImmediate
			l.stopSource = stopSourceExplicit
			cancel = true
		}
	case StopImmediate:
		// Immediate stop cannot be downgraded.
	}
	l.mu.Unlock()

	if cancel {
		l.runtimeCancel(nil)
	}
	l.notify()
	return nil
}

// Wait waits for loop termination. Canceling ctx only cancels this waiter; it
// does not stop the TurnLoop. Every successful return is cloned independently
// with agent.Runner's in-memory ownership semantics. Assistant-reported failures
// stop the loop after tool results settle, preserving inputs not yet delivered.
// Underlying errors remain available through errors.Is/errors.As; a provider
// failure without an error cause wraps ErrAssistantFailure. No inputs are retried.
func (l *TurnLoop) Wait(ctx context.Context) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("agent/turnloop: nil wait context")
	}
	select {
	case <-l.done:
		return l.detachedResult()
	default:
	}
	select {
	case <-l.done:
		return l.detachedResult()
	case <-ctx.Done():
		select {
		case <-l.done:
			return l.detachedResult()
		default:
			return Result{}, ctx.Err()
		}
	}
}

func validateQueueMode(name string, mode agent.QueueMode) error {
	if mode == agent.QueueModeOneAtATime || mode == agent.QueueModeAll {
		return nil
	}
	return fmt.Errorf("agent/turnloop: invalid %s queue mode %q", name, mode)
}

func validDelivery(delivery Delivery) bool {
	switch delivery {
	case DeliveryNextRun, DeliverySteering, DeliveryFollowUp:
		return true
	default:
		return false
	}
}

func validStopMode(mode StopMode) bool {
	return mode == StopGraceful || mode == StopImmediate
}

func (l *TurnLoop) notify() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func (l *TurnLoop) watchParent(parent context.Context) {
	select {
	case <-parent.Done():
		l.stopForParent(parentContextError(parent))
	case <-l.done:
	}
}

func (l *TurnLoop) stopForParent(err error) {
	l.mu.Lock()
	if !l.finished {
		switch l.requestedStop {
		case "", StopGraceful:
			l.accepting = false
			l.requestedStop = StopImmediate
			l.stopSource = stopSourceParent
			l.parentErr = err
		case StopImmediate:
			// An explicit immediate stop that won the state transition remains
			// normal control flow rather than acquiring a later parent error.
		}
	}
	l.mu.Unlock()
	l.notify()
}

func (l *TurnLoop) run(snapshot agent.AgentSnapshot) {
	defer l.runtimeCancel(nil)
	for {
		l.mu.Lock()
		l.reconcileParentCancellationLocked()
		if l.requestedStop == StopImmediate {
			l.finishLocked(snapshot, l.parentErr, StopImmediate, l.stopReceiptError(l.parentErr))
			l.mu.Unlock()
			return
		}
		if len(l.queue) == 0 {
			if l.requestedStop == StopGraceful {
				l.finishLocked(snapshot, nil, StopGraceful, nil)
				l.mu.Unlock()
				return
			}
			l.mu.Unlock()
			<-l.wake
			continue
		}

		var invocation Invocation
		if l.prepareInvocation != nil {
			l.mu.Unlock()
			prepared, err := l.prepareInvocation(l.runtimeCtx)
			if err == nil && nilInvocation(prepared) {
				err = errors.New("agent/turnloop: PrepareInvocation returned nil invocation")
			}
			l.mu.Lock()
			l.reconcileParentCancellationLocked()
			if l.finishInvocationLocked(snapshot, err) {
				l.mu.Unlock()
				return
			}
			invocation = prepared
		}
		// Keep original relative positions, not just the selected prompt batch:
		// QueueModeAll can remove matching inputs across other delivery classes.
		var originalQueue []queuedInput
		if invocation != nil {
			originalQueue = append([]queuedInput(nil), l.queue...)
		}
		input := l.queue[0]
		l.queue[0] = queuedInput{}
		l.queue = l.queue[1:]
		// Reserved inputs are not handed off until the start result is known.
		reserved := []*Receipt{input.receipt}
		prompts := []agent.Message{input.Message}
		switch input.Delivery {
		case DeliverySteering:
			if l.steeringMode == agent.QueueModeAll {
				prompts = append(prompts, l.takeDeliveryLocked(DeliverySteering, agent.QueueModeAll, &reserved)...)
			}
		case DeliveryFollowUp:
			if l.followUpMode == agent.QueueModeAll {
				prompts = append(prompts, l.takeDeliveryLocked(DeliveryFollowUp, agent.QueueModeAll, &reserved)...)
			}
		}
		remainingBeforeStart := len(l.queue)
		future := newReceiptFuture()
		l.active = &invocationState{future: future, skipInitialSteering: input.Delivery == DeliverySteering}
		l.mu.Unlock()

		hooks := agent.LoopHooks{GetSteeringMessages: l.getSteeringMessages, GetFollowUpMessages: l.getFollowUpMessages}
		var stream *agent.RunStream
		var startErr error
		if invocation == nil {
			stream = l.runner.RunWithHooks(l.runtimeCtx, snapshot, prompts, hooks)
		} else {
			stream, startErr = invocation.RunWithHooks(l.runtimeCtx, l.runner, cloneValue(prompts), hooks)
			if stream == nil && startErr == nil {
				startErr = errors.New("agent/turnloop: invocation returned neither stream nor error")
			}
		}
		if startErr != nil {
			// A non-nil stream means execution may have started: cancel and settle it,
			// never return its prompts as unhandled or automatically replay them.
			if stream != nil {
				_ = stream.Close()
				next, waitErr := stream.Wait()
				snapshot = next
				startErr = errors.Join(startErr, waitErr, errors.New("agent/turnloop: invocation returned both stream and error"))
			}
			l.mu.Lock()
			if stream == nil && !l.active.hookPolled {
				l.queue = append(originalQueue, l.queue[remainingBeforeStart:]...)
			} else if stream == nil {
				startErr = errors.Join(startErr, errors.New("agent/turnloop: invocation called hooks before returning a start error"))
			}
			if stream != nil || l.active.hookPolled {
				for _, receipt := range reserved {
					receipt.bind(future)
				}
				future.settle(ReceiptResult{Disposition: DispositionUncertain, Err: startErr})
			}
			l.reconcileParentCancellationLocked()
			l.active = nil
			l.finishInvocationLocked(snapshot, startErr)
			l.mu.Unlock()
			return
		}
		for _, receipt := range reserved {
			receipt.bind(future)
		}
		reserved = nil
		originalQueue = nil
		var runID string
		var assistantErr error
		for event := range stream.Events() {
			if runID == "" && event.Type == agent.EventAgentStart {
				runID = event.RunID
			}
			if failure := assistantFailure(event); failure != nil {
				assistantErr = errors.Join(assistantErr, failure)
			}
			if l.onEvent != nil {
				l.onEvent(event)
			}
		}
		next, runErr := stream.Wait()
		if assistantErr != nil {
			runErr = errors.Join(runErr, assistantErr)
		}
		snapshot = next
		future.settle(ReceiptResult{Disposition: DispositionHandedOff, RunID: runID, Err: runErr})

		l.mu.Lock()
		l.reconcileParentCancellationLocked()
		l.active = nil
		if l.finishInvocationLocked(snapshot, runErr) {
			l.mu.Unlock()
			return
		}
		l.mu.Unlock()
	}
}

func (l *TurnLoop) finishTurn(ctx context.Context, input agent.AgentTurnContext) (agent.AgentTurnDecision, error) {
	l.mu.Lock()
	state := l.active
	if state != nil {
		state.completedTurns++
	}
	l.mu.Unlock()

	if l.originalFinish != nil {
		return l.originalFinish(ctx, input)
	}
	return agent.AgentTurnDecision{}, nil
}

func (l *TurnLoop) getSteeringMessages(ctx context.Context) ([]agent.Message, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active != nil {
		l.active.hookPolled = true
	}
	// A parent cancellation may be visible before watchParent is scheduled.
	// Also honor a host-owned invocation's narrower context without treating
	// that local cancellation as a global parent stop.
	l.reconcileParentCancellationLocked()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !l.mayDequeueLocked() {
		return nil, nil
	}
	if l.active.skipInitialSteering {
		l.active.skipInitialSteering = false
		return nil, nil
	}
	return l.takeDeliveryLocked(DeliverySteering, l.steeringMode, nil), nil
}

func (l *TurnLoop) getFollowUpMessages(ctx context.Context) ([]agent.Message, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active != nil {
		l.active.hookPolled = true
	}
	// A parent cancellation may be visible before watchParent is scheduled.
	// Also honor a host-owned invocation's narrower context without treating
	// that local cancellation as a global parent stop.
	l.reconcileParentCancellationLocked()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !l.mayDequeueLocked() {
		return nil, nil
	}
	if messages := l.takeDeliveryLocked(DeliverySteering, l.steeringMode, nil); len(messages) != 0 {
		return messages, nil
	}
	return l.takeDeliveryLocked(DeliveryFollowUp, l.followUpMode, nil), nil
}

func (l *TurnLoop) mayDequeueLocked() bool {
	if l.requestedStop == StopImmediate || l.active == nil {
		return false
	}
	return l.maxTurns <= 0 || l.active.completedTurns < l.maxTurns
}

func (l *TurnLoop) takeDeliveryLocked(delivery Delivery, mode agent.QueueMode, reserved *[]*Receipt) []agent.Message {
	limit := 1
	if mode == agent.QueueModeAll {
		limit = len(l.queue)
	}
	if limit == 0 {
		return nil
	}

	messages := make([]agent.Message, 0, limit)
	remaining := l.queue[:0]
	for _, input := range l.queue {
		if input.Delivery == delivery && len(messages) < limit {
			messages = append(messages, input.Message)
			if reserved != nil {
				*reserved = append(*reserved, input.receipt)
			} else {
				input.receipt.bind(l.active.future)
			}
			continue
		}
		remaining = append(remaining, input)
	}
	if len(messages) == 0 {
		return nil
	}
	clear(l.queue[len(remaining):])
	l.queue = remaining
	return messages
}

func (l *TurnLoop) finishLocked(snapshot agent.AgentSnapshot, terminalErr error, mode StopMode, receiptErr error) {
	unhandled := make([]Input, len(l.queue))
	future := newReceiptFuture()
	future.settle(ReceiptResult{Disposition: DispositionUnhandled, Err: receiptErr})
	for i, input := range l.queue {
		unhandled[i] = input.Input
		input.receipt.bind(future)
	}
	l.accepting = false
	l.finished = true
	l.terminalResult = Result{
		Snapshot:  cloneValue(snapshot),
		Unhandled: cloneValue(unhandled),
		StopMode:  mode,
	}
	l.queue = nil
	l.terminalErr = terminalErr
	close(l.done)
}

func (l *TurnLoop) detachedResult() (Result, error) {
	l.mu.Lock()
	result := cloneValue(l.terminalResult)
	err := l.terminalErr
	l.mu.Unlock()
	return result, err
}

func (l *TurnLoop) reconcileParentCancellationLocked() {
	if l.finished || l.stopSource == stopSourceParent ||
		(l.stopSource == stopSourceExplicit && l.requestedStop == StopImmediate) {
		return
	}
	if l.parentCtx.Err() == nil {
		return
	}
	l.accepting = false
	l.requestedStop = StopImmediate
	l.stopSource = stopSourceParent
	if l.parentErr == nil {
		l.parentErr = parentContextError(l.parentCtx)
	}
}

func parentContextError(ctx context.Context) error {
	err := ctx.Err()
	cause := context.Cause(ctx)
	if err == nil {
		return cause
	}
	if cause == nil || cause == err {
		return err
	}
	return errors.Join(err, cause)
}

func cancellationOnly(err error, accepted ...error) bool {
	if err == nil {
		return false
	}
	if many, ok := err.(interface{ Unwrap() []error }); ok {
		causes := many.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !cancellationOnly(cause, accepted...) {
				return false
			}
		}
		return true
	}
	if cause := errors.Unwrap(err); cause != nil {
		return cancellationOnly(cause, accepted...)
	}
	if err == context.Canceled || err == context.DeadlineExceeded {
		return true
	}
	for _, cause := range accepted {
		if cause != nil && errors.Is(err, cause) {
			return true
		}
	}
	return false
}

// assistantFailure reads only events from the current invocation, not old
// failures in its conversation history. MessageEnd happens before error tool
// results are settled; the caller must still drain and Wait before stopping.
func assistantFailure(event agent.AgentEvent) error {
	if event.Type != agent.EventMessageEnd || event.Message == nil || event.Message.Role != agent.RoleAssistant {
		return nil
	}
	message := event.Message
	if message.StopReason != agent.StopReasonError && message.StopReason != agent.StopReasonAborted && message.ErrorMessage == "" {
		return nil
	}
	if event.Err != nil {
		return event.Err
	}
	if message.ErrorMessage != "" {
		return fmt.Errorf("%w: %s", ErrAssistantFailure, message.ErrorMessage)
	}
	return fmt.Errorf("%w: stop reason %s", ErrAssistantFailure, message.StopReason)
}

// finishInvocationLocked applies the same terminal policy to preparation,
// synchronous start failures, and completed runs. Caller holds mu.
func (l *TurnLoop) finishInvocationLocked(snapshot agent.AgentSnapshot, runErr error) bool {
	mode := l.requestedStop
	source := l.stopSource
	parentErr := l.parentErr
	if runErr != nil {
		terminalErr := runErr
		resultMode := StopMode("")
		switch {
		case source == stopSourceParent:
			resultMode = StopImmediate
			if cancellationOnly(runErr, context.Cause(l.runtimeCtx)) {
				terminalErr = parentErr
			} else if parentErr != nil {
				terminalErr = errors.Join(runErr, parentErr)
			}
		case mode == StopImmediate && source == stopSourceExplicit && cancellationOnly(runErr, context.Cause(l.runtimeCtx)):
			resultMode = StopImmediate
			terminalErr = nil
		case mode == StopImmediate:
			resultMode = StopImmediate
		}
		l.finishLocked(snapshot, terminalErr, resultMode, l.stopReceiptError(runErr))
		return true
	}
	if mode == StopImmediate {
		terminalErr := error(nil)
		if source == stopSourceParent {
			terminalErr = parentErr
		}
		l.finishLocked(snapshot, terminalErr, StopImmediate, l.stopReceiptError(runErr))
		return true
	}
	return false
}

func nilInvocation(invocation Invocation) bool {
	if invocation == nil {
		return true
	}
	value := reflect.ValueOf(invocation)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	}
	return false
}

// stopReceiptError preserves the cause before the public loop control-flow
// policy may normalize an explicit immediate cancellation to a nil Wait error.
// Caller holds mu.
func (l *TurnLoop) stopReceiptError(err error) error {
	if l.parentErr != nil {
		return errors.Join(err, l.parentErr)
	}
	if err == nil && l.requestedStop == StopImmediate {
		return context.Canceled
	}
	return err
}
