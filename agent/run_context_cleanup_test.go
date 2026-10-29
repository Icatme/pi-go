package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAgentCompletedRunCancelsInvocationContext(t *testing.T) {
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	var captured context.Context
	a, err := NewAgent(AgentDefinition{Model: StreamFunc(func(ctx context.Context, _ ModelRequest) (AssistantStream, error) {
		captured = ctx
		return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Parts: []Part{NewTextPart("answer")}}, nil), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.PromptText(parent, "request"); err != nil {
		t.Fatal(err)
	}
	if captured == nil {
		t.Fatal("model not called")
	}
	select {
	case <-captured.Done():
	default:
		t.Fatal("completed invocation context remains live")
	}
	if parent.Err() != nil {
		t.Fatal("completion canceled parent")
	}
	if a.State().Error != "" {
		t.Fatal("cleanup turned success into failure")
	}
}

// Observe the documented AfterFunc parent hook without inspecting context's
// internal child map. A finished invocation must unregister its parent hook.
type cleanupTrackingParent struct {
	context.Context
	done   chan struct{}
	mu     sync.Mutex
	active int
	onStop func()
}

func (p *cleanupTrackingParent) Done() <-chan struct{} { return p.done }
func (p *cleanupTrackingParent) AfterFunc(func()) func() bool {
	p.mu.Lock()
	p.active++
	p.mu.Unlock()
	var once sync.Once
	return func() bool {
		stopped := false
		once.Do(func() {
			if p.onStop != nil {
				p.onStop()
			}
			p.mu.Lock()
			p.active--
			p.mu.Unlock()
			stopped = true
		})
		return stopped
	}
}
func (p *cleanupTrackingParent) count() int { p.mu.Lock(); defer p.mu.Unlock(); return p.active }

func TestAgentRunUnregistersParentOnEveryExit(t *testing.T) {
	for _, mode := range []string{"prompt-success", "prompt-error", "continue-success", "continue-error", "continue-empty", "continue-assistant"} {
		t.Run(mode, func(t *testing.T) {
			parent := &cleanupTrackingParent{Context: context.Background(), done: make(chan struct{})}
			cause := errors.New("model failure")
			a, err := NewAgent(AgentDefinition{Model: StreamFunc(func(context.Context, ModelRequest) (AssistantStream, error) {
				if strings.HasSuffix(mode, "-error") {
					return nil, cause
				}
				return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Parts: []Part{NewTextPart("answer")}}, nil), nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				if strings.HasPrefix(mode, "prompt-") {
					err = a.PromptText(parent, "request")
				} else {
					switch mode {
					case "continue-empty":
						a.ReplaceMessages(nil)
					case "continue-assistant":
						a.ReplaceMessages([]Message{NewTextMessage(RoleAssistant, "answer")})
					default:
						a.ReplaceMessages([]Message{NewUserTextMessage("request")})
					}
					err = a.Continue(parent)
				}
				switch mode {
				case "continue-empty":
					if !errors.Is(err, ErrNoMessagesToContinue) {
						t.Fatal(err)
					}
				case "continue-assistant":
					if !errors.Is(err, ErrCannotContinueFromAssistant) {
						t.Fatal(err)
					}
				default:
					if err != nil {
						t.Fatal(err)
					}
				}
				if n := parent.count(); n != 0 {
					t.Fatalf("retained %d parent hooks after invocation %d", n, i)
				}
				if parent.Err() != nil {
					t.Fatal("parent canceled")
				}
				if err := a.WaitForIdle(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestAgentContextCleanupAllowsReentrantParentStop(t *testing.T) {
	parent := &cleanupTrackingParent{Context: context.Background(), done: make(chan struct{})}
	a, err := NewAgent(AgentDefinition{Model: StreamFunc(func(context.Context, ModelRequest) (AssistantStream, error) {
		return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Parts: []Part{NewTextPart("answer")}}, nil), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	var callbackErr error
	calls := 0
	parent.onStop = func() {
		calls++
		_ = a.State()
		callbackErr = a.PromptText(context.Background(), "must not begin before previous cleanup")
	}
	done := make(chan error, 1)
	go func() { done <- a.PromptText(parent, "request") }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("parent stop callback deadlocked")
	}
	if calls != 1 || !errors.Is(callbackErr, ErrAlreadyRunning) {
		t.Fatalf("cleanup allowed new invocation or did not run: calls=%d err=%v", calls, callbackErr)
	}
	if n := parent.count(); n != 0 {
		t.Fatalf("retained %d callbacks", n)
	}
}
