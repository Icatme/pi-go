package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

const runnerEventBufferSize = 32

var runnerRunIDFallback atomic.Uint64

// Runner starts independent agent runs from caller-owned snapshots.
// It is safe to reuse concurrently because it does not retain run state.
type Runner struct {
	definition AgentDefinition
}

type runnerRunIDContextKey struct{}

// NewRunner validates and snapshots an agent definition for stateless runs.
func NewRunner(definition AgentDefinition) (*Runner, error) {
	validated, err := definition.Validate()
	if err != nil {
		return nil, err
	}
	return &Runner{definition: cloneRunnerDefinition(validated)}, nil
}

// Run starts a new run from snapshot and prompts.
//
// Events are delivered losslessly through a bounded channel. Callers must
// drain Events before Wait, or call Close to cancel and drain an abandoned run.
func (r *Runner) Run(ctx context.Context, snapshot AgentSnapshot, prompts []Message) *RunStream {
	return r.RunWithHooks(ctx, snapshot, prompts, LoopHooks{})
}

// RunWithHooks starts a new run with invocation-local runtime hooks.
func (r *Runner) RunWithHooks(ctx context.Context, snapshot AgentSnapshot, prompts []Message, hooks LoopHooks) *RunStream {
	return r.start(ctx, snapshot, prompts, runnerStartRun, hooks)
}

// Query starts a new run from an empty snapshot with one user-text prompt.
func (r *Runner) Query(ctx context.Context, query string) *RunStream {
	return r.Run(ctx, AgentSnapshot{}, []Message{NewUserTextMessage(query)})
}

// Continue resumes a run from snapshot without appending prompt messages.
func (r *Runner) Continue(ctx context.Context, snapshot AgentSnapshot) *RunStream {
	return r.start(ctx, snapshot, nil, runnerStartContinue, LoopHooks{})
}

// ResumePendingToolCallsWithHooks resumes the exact pending tool-call batch in
// snapshot with invocation-local runtime hooks.
func (r *Runner) ResumePendingToolCallsWithHooks(ctx context.Context, snapshot AgentSnapshot, hooks LoopHooks) *RunStream {
	return r.start(ctx, snapshot, nil, runnerStartPendingTools, hooks)
}

// RunStream exposes one asynchronous runner invocation.
type RunStream struct {
	events chan AgentEvent
	done   chan struct{}
	cancel context.CancelFunc

	closeOnce sync.Once
	resultMu  sync.RWMutex
	snapshot  AgentSnapshot
	err       error
}

// Events returns the lossless, bounded event stream for this run.
func (s *RunStream) Events() <-chan AgentEvent {
	return s.events
}

// Wait waits for completion and returns an isolated final snapshot.
// Callers must drain Events concurrently or close an abandoned stream first.
func (s *RunStream) Wait() (AgentSnapshot, error) {
	<-s.done
	s.resultMu.RLock()
	snapshot := cloneSnapshotValue(s.snapshot)
	err := s.err
	s.resultMu.RUnlock()
	return snapshot, err
}

// Close cancels the run, drains outstanding events, and waits for shutdown.
// It is safe to call repeatedly or concurrently with Wait and other Close calls.
func (s *RunStream) Close() error {
	s.closeOnce.Do(s.cancel)
	for range s.events {
	}
	<-s.done
	return nil
}

type runnerStartMode uint8

const (
	runnerStartRun runnerStartMode = iota
	runnerStartContinue
	runnerStartPendingTools
)

func (r *Runner) start(ctx context.Context, snapshot AgentSnapshot, prompts []Message, mode runnerStartMode, hooks LoopHooks) *RunStream {
	parentRunID := runnerRunIDFromContext(ctx)
	runID := newRunnerRunID()
	runCtx, cancel := context.WithCancel(ctx)
	runCtx = context.WithValue(runCtx, runnerRunIDContextKey{}, runID)
	stream := &RunStream{
		events: make(chan AgentEvent, runnerEventBufferSize),
		done:   make(chan struct{}),
		cancel: cancel,
	}
	definition := cloneRunnerDefinition(r.definition)
	initial := cloneSnapshotValue(snapshot)
	normalizedPrompts := cloneMessages(prompts)
	publisher := &runnerEventPublisher{
		events:      stream.events,
		runID:       runID,
		parentRunID: parentRunID,
		agentName:   definition.Name,
	}

	go func() {
		defer cancel()

		var (
			next *AgentSnapshot
			err  error
		)
		switch mode {
		case runnerStartContinue:
			next, err = NewEngine().Continue(runCtx, definition, &initial, publisher.emit)
		case runnerStartPendingTools:
			next, err = NewEngine().ResumePendingToolCallsWithHooks(runCtx, definition, &initial, publisher.emit, hooks)
		default:
			next, err = NewEngine().RunWithHooks(runCtx, definition, &initial, normalizedPrompts, publisher.emit, hooks)
		}

		finalSnapshot := initial
		if next != nil {
			finalSnapshot = cloneSnapshotValue(*next)
		}
		publisher.finish(err)

		stream.resultMu.Lock()
		stream.snapshot = finalSnapshot
		stream.err = err
		stream.resultMu.Unlock()
		close(stream.events)
		close(stream.done)
	}()

	return stream
}

type runnerEventPublisher struct {
	mu          sync.Mutex
	events      chan<- AgentEvent
	runID       string
	parentRunID string
	agentName   string
	sequence    uint64
	started     bool
	ended       bool
}

func (p *runnerEventPublisher) emit(event AgentEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ended {
		return
	}
	if event.Type != EventAgentStart && !p.started {
		p.publishLocked(AgentEvent{Type: EventAgentStart})
		p.started = true
	}

	switch event.Type {
	case EventAgentStart:
		if p.started {
			return
		}
		p.started = true
	case EventAgentEnd:
		if p.ended {
			return
		}
		p.ended = true
	}
	p.publishLocked(event)
}

func (p *runnerEventPublisher) finish(runErr error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.started {
		p.publishLocked(AgentEvent{Type: EventAgentStart})
		p.started = true
	}
	if p.ended {
		return
	}
	end := AgentEvent{Type: EventAgentEnd, Err: runErr}
	if messages, _ := engineRunErrorContext(runErr); len(messages) > 0 {
		end.Messages = messages
	}
	p.ended = true
	p.publishLocked(end)
}

func (p *runnerEventPublisher) publishLocked(event AgentEvent) {
	p.sequence++
	event = cloneAgentEvent(event)
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	event.RunID = p.runID
	event.ParentRunID = p.parentRunID
	event.Sequence = p.sequence
	event.AgentName = p.agentName
	p.events <- event
}

func runnerRunIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	runID, _ := ctx.Value(runnerRunIDContextKey{}).(string)
	return runID
}

func cloneRunnerDefinition(definition AgentDefinition) AgentDefinition {
	cloned := definition
	cloned.DefaultModel = cloneModelRef(definition.DefaultModel)
	cloned.ThinkingBudgets = cloneThinkingBudgets(definition.ThinkingBudgets)
	cloned.Tools = cloneTools(definition.Tools)
	return cloned
}

func newRunnerRunID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("run-%x-%x", time.Now().UTC().UnixNano(), runnerRunIDFallback.Add(1))
}
