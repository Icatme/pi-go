package langgraphgo

import (
	"context"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
)

func TestNewSessionNodePromptClearsPrompts(t *testing.T) {
	t.Parallel()

	type state struct {
		Snapshot agent.AgentSnapshot
		Prompts  []agent.Message
	}

	node := NewSessionNode(nil, agent.AgentDefinition{
		Model: staticModel{
			streamFn: func(_ context.Context, _ agent.ModelRequest) (agent.AssistantStream, error) {
				return newStaticAssistantStream(agent.Message{
					Role:       agent.RoleAssistant,
					Parts:      []agent.Part{{Type: agent.PartTypeText, Text: "hello"}},
					Timestamp:  time.Now().UTC(),
					StopReason: agent.StopReasonStop,
				}, nil), nil
			},
		},
	}, Binder[state]{
		GetSnapshot: func(s state) agent.AgentSnapshot { return s.Snapshot },
		SetSnapshot: func(s state, snapshot agent.AgentSnapshot) state {
			s.Snapshot = snapshot
			return s
		},
		GetPrompts: func(s state) []agent.Message { return s.Prompts },
		SetPrompts: func(s state, prompts []agent.Message) state {
			s.Prompts = prompts
			return s
		},
	})

	next, err := node(context.Background(), state{
		Prompts: []agent.Message{agent.NewTextMessage(agent.RoleUser, "hi")},
	})
	if err != nil {
		t.Fatalf("node returned error: %v", err)
	}
	if len(next.Prompts) != 0 {
		t.Fatalf("expected prompts to be cleared, got %d", len(next.Prompts))
	}
	if got := len(next.Snapshot.Messages); got != 2 {
		t.Fatalf("expected 2 messages, got %d", got)
	}
}

func TestNewSessionNodeContinueMode(t *testing.T) {
	t.Parallel()

	type state struct {
		Snapshot agent.AgentSnapshot
		Prompts  []agent.Message
	}

	node := NewSessionNode(nil, agent.AgentDefinition{
		Model: staticModel{
			streamFn: func(_ context.Context, request agent.ModelRequest) (agent.AssistantStream, error) {
				if len(request.Messages) != 1 || request.Messages[0].Role != agent.RoleUser {
					t.Fatalf("expected continue to reuse existing user message, got %+v", request.Messages)
				}
				return newStaticAssistantStream(agent.Message{
					Role:       agent.RoleAssistant,
					Parts:      []agent.Part{{Type: agent.PartTypeText, Text: "continued"}},
					Timestamp:  time.Now().UTC(),
					StopReason: agent.StopReasonStop,
				}, nil), nil
			},
		},
	}, Binder[state]{
		GetSnapshot: func(s state) agent.AgentSnapshot { return s.Snapshot },
		SetSnapshot: func(s state, snapshot agent.AgentSnapshot) state {
			s.Snapshot = snapshot
			return s
		},
		GetPrompts: func(s state) []agent.Message { return s.Prompts },
	})

	next, err := node(context.Background(), state{
		Snapshot: agent.AgentSnapshot{
			Messages: []agent.Message{agent.NewTextMessage(agent.RoleUser, "resume me")},
		},
	})
	if err != nil {
		t.Fatalf("node returned error: %v", err)
	}
	if got := len(next.Snapshot.Messages); got != 2 {
		t.Fatalf("expected 2 messages, got %d", got)
	}
	if next.Snapshot.Messages[1].Parts[0].Text != "continued" {
		t.Fatalf("unexpected assistant text %q", next.Snapshot.Messages[1].Parts[0].Text)
	}
}

type staticModel struct {
	streamFn func(context.Context, agent.ModelRequest) (agent.AssistantStream, error)
}

func (m staticModel) Stream(ctx context.Context, request agent.ModelRequest) (agent.AssistantStream, error) {
	return m.streamFn(ctx, request)
}

type staticAssistantStream struct {
	events  chan agent.AssistantEvent
	done    chan struct{}
	message agent.Message
	err     error
}

func newStaticAssistantStream(message agent.Message, events []agent.AssistantEvent) *staticAssistantStream {
	stream := &staticAssistantStream{
		events:  make(chan agent.AssistantEvent, len(events)),
		done:    make(chan struct{}),
		message: message,
	}
	go func() {
		for _, event := range events {
			stream.events <- event
		}
		close(stream.events)
		close(stream.done)
	}()
	return stream
}

func (s *staticAssistantStream) Events() <-chan agent.AssistantEvent {
	return s.events
}

func (s *staticAssistantStream) Wait() (agent.Message, error) {
	<-s.done
	return s.message, s.err
}
