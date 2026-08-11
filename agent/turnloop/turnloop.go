// Package turnloop provides a bounded, concurrent input loop around agent.Runner.
package turnloop

import (
	"context"
	"errors"
	"fmt"
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

// Config configures a TurnLoop.
type Config struct {
	Definition    agent.AgentDefinition
	Initial       agent.AgentSnapshot
	QueueCapacity int
	OnEvent       agent.EventSink
}

// Result is the terminal state of a TurnLoop, cloned with the same in-memory
// ownership semantics as agent.Runner.
type Result struct {
	Snapshot  agent.AgentSnapshot
	Unhandled []Input
	// StopMode is empty when an ordinary Runner error, rather than a completed
	// stop request or parent cancellation, terminated the loop.
	StopMode StopMode
}

type stopSource uint8

const (
	stopSourceNone stopSource = iota
	stopSourceExplicit
	stopSourceParent
)

type invocationState struct {
	completedTurns      int
	skipInitialSteering bool
}

// TurnLoop serially feeds admitted inputs through independent agent.Runner
// invocations. Its methods are safe for concurrent use.
type TurnLoop struct {
	mu sync.Mutex

	runner         *agent.Runner
	queueCapacity  int
	queue          []Input
	steeringMode   agent.QueueMode
	followUpMode   agent.QueueMode
	maxTurns       int
	originalStop   agent.ShouldStopAfterTurnHook
	onEvent        agent.EventSink
	active         *invocationState
	accepting      bool
	finished       bool
	requestedStop  StopMode
	stopSource     stopSource
	parentErr      error
	terminalResult Result
	terminalErr    error

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
		queueCapacity: config.QueueCapacity,
		steeringMode:  definition.SteeringMode,
		followUpMode:  definition.FollowUpMode,
		maxTurns:      definition.MaxTurns,
		originalStop:  definition.ShouldStopAfterTurn,
		onEvent:       config.OnEvent,
		accepting:     true,
		runtimeCtx:    runtimeCtx,
		runtimeCancel: runtimeCancel,
		parentCtx:     ctx,
		wake:          make(chan struct{}, 1),
		done:          make(chan struct{}),
	}
	definition.ShouldStopAfterTurn = loop.shouldStopAfterTurn
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
	l.queue = append(l.queue, detached)
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
// with agent.Runner's in-memory ownership semantics.
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
			l.finishLocked(snapshot, l.parentErr, StopImmediate)
			l.mu.Unlock()
			return
		}
		if len(l.queue) == 0 {
			if l.requestedStop == StopGraceful {
				l.finishLocked(snapshot, nil, StopGraceful)
				l.mu.Unlock()
				return
			}
			l.mu.Unlock()
			<-l.wake
			continue
		}

		input := l.queue[0]
		l.queue[0] = Input{}
		l.queue = l.queue[1:]
		prompts := []agent.Message{input.Message}
		switch input.Delivery {
		case DeliverySteering:
			if l.steeringMode == agent.QueueModeAll {
				prompts = append(prompts, l.takeDeliveryLocked(DeliverySteering, agent.QueueModeAll)...)
			}
		case DeliveryFollowUp:
			if l.followUpMode == agent.QueueModeAll {
				prompts = append(prompts, l.takeDeliveryLocked(DeliveryFollowUp, agent.QueueModeAll)...)
			}
		}
		l.active = &invocationState{skipInitialSteering: input.Delivery == DeliverySteering}
		l.mu.Unlock()

		stream := l.runner.RunWithHooks(l.runtimeCtx, snapshot, prompts, agent.LoopHooks{
			GetSteeringMessages: l.getSteeringMessages,
			GetFollowUpMessages: l.getFollowUpMessages,
		})
		for event := range stream.Events() {
			if l.onEvent != nil {
				l.onEvent(event)
			}
		}
		next, runErr := stream.Wait()
		snapshot = next

		l.mu.Lock()
		l.reconcileParentCancellationLocked()
		l.active = nil
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
			l.finishLocked(snapshot, terminalErr, resultMode)
			l.mu.Unlock()
			return
		}
		if mode == StopImmediate {
			terminalErr := error(nil)
			if source == stopSourceParent {
				terminalErr = parentErr
			}
			l.finishLocked(snapshot, terminalErr, StopImmediate)
			l.mu.Unlock()
			return
		}
		l.mu.Unlock()
	}
}

func (l *TurnLoop) shouldStopAfterTurn(ctx context.Context, input agent.ShouldStopAfterTurnContext) (bool, error) {
	l.mu.Lock()
	state := l.active
	if state != nil {
		state.completedTurns++
	}
	l.mu.Unlock()

	if l.originalStop != nil {
		stop, err := l.originalStop(ctx, input)
		if err != nil || stop {
			return stop, err
		}
	}
	return false, nil
}

func (l *TurnLoop) getSteeringMessages(context.Context) ([]agent.Message, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.mayDequeueLocked() {
		return nil, nil
	}
	if l.active.skipInitialSteering {
		l.active.skipInitialSteering = false
		return nil, nil
	}
	return l.takeDeliveryLocked(DeliverySteering, l.steeringMode), nil
}

func (l *TurnLoop) getFollowUpMessages(context.Context) ([]agent.Message, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.mayDequeueLocked() {
		return nil, nil
	}
	if messages := l.takeDeliveryLocked(DeliverySteering, l.steeringMode); len(messages) != 0 {
		return messages, nil
	}
	return l.takeDeliveryLocked(DeliveryFollowUp, l.followUpMode), nil
}

func (l *TurnLoop) mayDequeueLocked() bool {
	if l.requestedStop == StopImmediate || l.active == nil {
		return false
	}
	return l.maxTurns <= 0 || l.active.completedTurns < l.maxTurns
}

func (l *TurnLoop) takeDeliveryLocked(delivery Delivery, mode agent.QueueMode) []agent.Message {
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

func (l *TurnLoop) finishLocked(snapshot agent.AgentSnapshot, terminalErr error, mode StopMode) {
	l.accepting = false
	l.finished = true
	l.terminalResult = Result{
		Snapshot:  cloneValue(snapshot),
		Unhandled: cloneValue(l.queue),
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
