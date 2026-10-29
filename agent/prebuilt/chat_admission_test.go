package prebuilt

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	core "github.com/Icatme/pi-go/agent"
)

type blockingChatModel struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (m *blockingChatModel) Stream(ctx context.Context, _ core.ModelRequest) (core.AssistantStream, error) {
	m.once.Do(func() { close(m.entered) })
	select {
	case <-m.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	events := make(chan core.AssistantEvent)
	close(events)
	return &chatAssistantStream{events: events, final: core.Message{Role: core.RoleAssistant, Parts: []core.Part{{Type: core.PartTypeText, Text: "first response"}}, StopReason: core.StopReasonStop}}, nil
}

func TestChatAgentRejectsConcurrentAdmission(t *testing.T) {
	for _, first := range []string{"Chat", "AsyncChat", "AsyncChatWithChunks"} {
		t.Run(first, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			model := &blockingChatModel{entered: make(chan struct{}), release: make(chan struct{})}
			a, err := NewChatAgent(core.AgentDefinition{Model: model})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			start := func() error {
				switch first {
				case "Chat":
					_, err := a.Chat(ctx, "first")
					return err
				case "AsyncChat":
					ch, err := a.AsyncChat(ctx, "first")
					if err != nil {
						return err
					}
					for range ch {
					}
					return nil
				default:
					ch, err := a.AsyncChatWithChunks(ctx, "first")
					if err != nil {
						return err
					}
					for range ch {
					}
					return nil
				}
			}
			go func() { done <- start() }()
			select {
			case <-model.entered:
			case <-ctx.Done():
				t.Fatal("model did not start")
			}
			if _, err := a.Chat(ctx, "rejected sync"); !errors.Is(err, core.ErrAlreadyRunning) {
				t.Errorf("Chat error = %v, want ErrAlreadyRunning", err)
			}
			for name, call := range map[string]func(context.Context, string) (<-chan string, error){"AsyncChat": a.AsyncChat, "AsyncChatWithChunks": a.AsyncChatWithChunks} {
				ch, err := call(ctx, "rejected async")
				if !errors.Is(err, core.ErrAlreadyRunning) {
					t.Errorf("%s error = %v, want ErrAlreadyRunning", name, err)
				}
				if ch != nil {
					go func() {
						for range ch {
						}
					}()
					t.Errorf("%s returned stream for rejected invocation", name)
				}
			}
			close(model.release)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("first run did not finish")
			}
			if _, err := a.Chat(ctx, "next"); err != nil {
				t.Fatalf("admission not released: %v", err)
			}
			if got := len(a.agent.State().Messages); got != 4 {
				t.Errorf("history contains rejected input: %d messages, want 4", got)
			}
		})
	}
}

func TestChatAgentConcurrentToolUpdates(t *testing.T) {
	a, err := NewChatAgent(core.AgentDefinition{Model: &chatScriptedModel{}, Tools: []core.ToolDefinition{{Name: "base"}}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				a.SetTools([]core.ToolDefinition{{Name: "dynamic", Parameters: map[string]any{"type": "object"}}})
				a.AddTool(core.ToolDefinition{Name: "extra"})
				a.GetTools()
				a.RemoveTool("extra")
				a.ClearTools()
			}
		}()
	}
	wg.Wait()
	a.AddTool(core.ToolDefinition{Name: "final"})
	if got := a.GetTools(); len(got) != 1 || got[0].Name != "final" {
		t.Fatalf("unexpected dynamic tools: %#v", got)
	}
	if got := a.agent.State().Tools; len(got) != 2 || got[0].Name != "base" || got[1].Name != "final" {
		t.Fatalf("unexpected effective tools: %#v", got)
	}
}

func TestChatAgentAsyncAdmissionReleasedAfterCancellation(t *testing.T) {
	for _, chunks := range []bool{false, true} {
		t.Run(map[bool]string{false: "AsyncChat", true: "AsyncChatWithChunks"}[chunks], func(t *testing.T) {
			model := &blockingChatModel{entered: make(chan struct{}), release: make(chan struct{})}
			a, err := NewChatAgent(core.AgentDefinition{Model: model})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			call := a.AsyncChat
			if chunks {
				call = a.AsyncChatWithChunks
			}
			output, err := call(ctx, "cancel me")
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-model.entered:
			case <-ctx.Done():
				t.Fatal("model did not start")
			}
			cancel()
			for range output {
			}
			close(model.release)
			nextCtx, nextCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer nextCancel()
			if _, err := a.Chat(nextCtx, "next"); err != nil {
				t.Fatalf("admission leaked on cancellation: %v", err)
			}
		})
	}
}
