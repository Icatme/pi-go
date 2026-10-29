package prebuilt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	core "github.com/Icatme/pi-go/agent"
)

type terminalStatusError struct{ status int }

func (e *terminalStatusError) Error() string { return fmt.Sprintf("upstream HTTP %d", e.status) }

type terminalModel struct {
	events   []core.AssistantEvent
	startErr error
	final    core.Message
	waitErr  error
	closeErr error
}

func (m terminalModel) Stream(ctx context.Context, _ core.ModelRequest) (core.AssistantStream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.startErr != nil {
		return nil, m.startErr
	}
	events := make(chan core.AssistantEvent, len(m.events))
	for _, event := range m.events {
		events <- event
	}
	close(events)
	return terminalStream{chatAssistantStream: chatAssistantStream{events: events, final: m.final, err: m.waitErr}, closeErr: m.closeErr}, nil
}

type terminalStream struct {
	chatAssistantStream
	closeErr error
}

func (s terminalStream) Events() <-chan core.AssistantEvent { return s.events }
func (s terminalStream) Wait() (core.Message, error)        { return s.final, s.err }
func (s terminalStream) Close() error                       { return s.closeErr }

func TestChatAgentReportsTerminalFailures(t *testing.T) {
	upstream := &terminalStatusError{502}
	waitErr := errors.New("wait failed")
	closeErr := errors.New("close failed")
	prepareErr := errors.New("prepare failed")
	partial := core.Message{Role: core.RoleAssistant, Parts: []core.Part{{Type: core.PartTypeText, Text: "partial"}}, StopReason: core.StopReasonStop}
	cases := []struct {
		name    string
		model   terminalModel
		prepare bool
		cancel  bool
		wants   []error
		text    string
	}{
		{name: "502", model: terminalModel{startErr: upstream}, wants: []error{upstream}},
		{name: "cancel", cancel: true, wants: []error{context.Canceled}},
		{name: "wait", model: terminalModel{final: partial, waitErr: waitErr}, wants: []error{waitErr}, text: "partial"},
		{name: "wait-close-cancel-join", model: terminalModel{final: partial, waitErr: errors.Join(context.Canceled, waitErr), closeErr: closeErr}, wants: []error{context.Canceled, waitErr, closeErr}, text: "partial"},
		{name: "prepare", prepare: true, wants: []error{prepareErr}},
		{name: "provider-error", model: terminalModel{final: core.Message{Role: core.RoleAssistant, StopReason: core.StopReasonError, ErrorMessage: "provider failure"}}},
		{name: "provider-empty-error", model: terminalModel{final: core.Message{Role: core.RoleAssistant, StopReason: core.StopReasonError}}},
		{name: "provider-empty-aborted", model: terminalModel{final: core.Message{Role: core.RoleAssistant, StopReason: core.StopReasonAborted}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, print := range []bool{false, true} {
				ctx := context.Background()
				if tc.cancel {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				}
				def := core.AgentDefinition{Model: tc.model}
				if tc.prepare {
					def.PrepareRequest = func(context.Context, core.PrepareRequestContext) (*core.AgentLoopTurnUpdate, error) {
						return nil, prepareErr
					}
				}
				a, err := NewChatAgent(def)
				if err != nil {
					t.Fatal(err)
				}
				var text string
				if print {
					err = a.PrintStream(ctx, "hello", func(chunk string) error { text += chunk; return nil })
				} else {
					text, err = a.Chat(ctx, "hello")
				}
				if err == nil {
					t.Fatalf("print=%v: failure reported success", print)
				}
				if text != tc.text {
					t.Errorf("print=%v: text=%q want %q", print, text, tc.text)
				}
				for _, cause := range tc.wants {
					if !errors.Is(err, cause) {
						t.Errorf("print=%v: error %v lost cause %v", print, err, cause)
					}
				}
				if tc.name == "provider-empty-aborted" && errors.Is(err, context.Canceled) {
					t.Errorf("fabricated context cancellation: %v", err)
				}
				if tc.name == "502" {
					var status *terminalStatusError
					if !errors.As(err, &status) || status.status != 502 {
						t.Errorf("lost typed 502: %v", err)
					}
				}
			}
		})
	}
}

func TestChatAgentTerminalFailureDoesNotLeakToNextRun(t *testing.T) {
	model := &chatScriptedModel{responses: []chatScriptedResponse{
		{final: core.Message{Role: core.RoleAssistant, StopReason: core.StopReasonError, ErrorMessage: "first failed"}},
		{final: core.Message{Role: core.RoleAssistant, StopReason: core.StopReasonStop, Parts: []core.Part{{Type: core.PartTypeText, Text: "success"}}}},
	}}
	a, err := NewChatAgent(core.AgentDefinition{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Chat(context.Background(), "first"); err == nil {
		t.Fatal("first run should fail")
	}
	text, err := a.Chat(context.Background(), "second")
	if err != nil || text != "success" {
		t.Fatalf("second=(%q,%v)", text, err)
	}
	if err := a.PrintStream(context.Background(), "third", func(string) error { return nil }); err != nil {
		t.Fatalf("third=%v", err)
	}
}

func TestCoreTerminalEventsKeepCauseWithoutChangingPromptContract(t *testing.T) {
	cause := &terminalStatusError{502}
	closeCause := errors.New("close failed")
	for _, where := range []string{"start", "wait", "prepare", "joined"} {
		t.Run(where, func(t *testing.T) {
			model := terminalModel{}
			def := core.AgentDefinition{}
			switch where {
			case "start":
				model.startErr = cause
			case "wait":
				model.waitErr = cause
			case "joined":
				model.waitErr = errors.Join(context.Canceled, cause)
				model.closeErr = closeCause
			case "prepare":
				def.PrepareRequest = func(context.Context, core.PrepareRequestContext) (*core.AgentLoopTurnUpdate, error) {
					return nil, cause
				}
			}
			def.Model = model
			a, err := core.NewAgent(def)
			if err != nil {
				t.Fatal(err)
			}
			var terminalErr error
			a.Subscribe(func(event core.AgentEvent) {
				if event.Type == core.EventMessageEnd {
					terminalErr = event.Err
				}
			})
			if err := a.PromptText(context.Background(), "hello"); err != nil {
				t.Fatalf("core Prompt contract changed: %v", err)
			}
			if !errors.Is(terminalErr, cause) {
				t.Fatalf("event lost cause: %v", terminalErr)
			}
			var status *terminalStatusError
			if !errors.As(terminalErr, &status) {
				t.Fatalf("event lost type: %v", terminalErr)
			}
			if where == "joined" && (!errors.Is(terminalErr, context.Canceled) || !errors.Is(terminalErr, closeCause)) {
				t.Fatalf("event lost joined causes: %v", terminalErr)
			}
			if a.State().Error == "" {
				t.Fatal("state error contract changed")
			}
		})
	}
}

func TestChatAgentTextOnlyAsyncStillDeliversPartialOutput(t *testing.T) {
	for _, chunks := range []bool{false, true} {
		a, err := NewChatAgent(core.AgentDefinition{Model: terminalModel{
			final: core.Message{Role: core.RoleAssistant, Parts: []core.Part{{Type: core.PartTypeText, Text: "partial answer"}}}, waitErr: errors.New("failed"),
		}})
		if err != nil {
			t.Fatal(err)
		}
		call := a.AsyncChat
		if chunks {
			call = a.AsyncChatWithChunks
		}
		output, err := call(context.Background(), "hello")
		if err != nil {
			t.Fatal(err)
		}
		text := ""
		for part := range output {
			text += part
		}
		if text != "partial answer" {
			t.Errorf("chunks=%v: text=%q", chunks, text)
		}
	}
}

func TestCoreTerminalEventPreservesProviderFailureWithCancellation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		final       core.Message
		independent bool
	}{
		{"provider-error", core.Message{Role: core.RoleAssistant, StopReason: core.StopReasonError, ErrorMessage: "provider failed"}, true},
		{"empty-provider-error", core.Message{Role: core.RoleAssistant, StopReason: core.StopReasonError}, true},
		{"matching-cancellation", core.Message{Role: core.RoleAssistant, StopReason: core.StopReasonAborted, ErrorMessage: context.Canceled.Error()}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := core.NewAgent(core.AgentDefinition{Model: terminalModel{final: tc.final, waitErr: context.Canceled}})
			if err != nil {
				t.Fatal(err)
			}
			var terminalErr error
			a.Subscribe(func(event core.AgentEvent) {
				if event.Type == core.EventMessageEnd {
					terminalErr = event.Err
				}
			})
			if err := a.PromptText(context.Background(), "hello"); err != nil {
				t.Fatal(err)
			}
			if !errors.Is(terminalErr, context.Canceled) {
				t.Fatalf("lost cancellation: %v", terminalErr)
			}
			if got := containsNonCancellationCause(terminalErr); got != tc.independent {
				t.Fatalf("independent cause=%v want %v: %v", got, tc.independent, terminalErr)
			}
		})
	}
}

func containsNonCancellationCause(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if containsNonCancellationCause(cause) {
				return true
			}
		}
		return false
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return containsNonCancellationCause(wrapped.Unwrap())
	}
	return !errors.Is(err, context.Canceled)
}

func TestCoreTerminalEventPreservesFailedPartialWithCancellation(t *testing.T) {
	partial := core.Message{Role: core.RoleAssistant, StopReason: core.StopReasonError, ErrorMessage: "partial provider failure"}
	a, err := core.NewAgent(core.AgentDefinition{Model: terminalModel{
		events:  []core.AssistantEvent{{Type: core.AssistantEventStart, Message: partial}},
		waitErr: context.Canceled,
	}})
	if err != nil {
		t.Fatal(err)
	}
	var terminalErr error
	a.Subscribe(func(event core.AgentEvent) {
		if event.Type == core.EventMessageEnd {
			terminalErr = event.Err
		}
	})
	if err := a.PromptText(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(terminalErr, context.Canceled) || !containsNonCancellationCause(terminalErr) {
		t.Fatalf("lost partial provider cause: %v", terminalErr)
	}
	if !strings.Contains(terminalErr.Error(), "partial provider failure") {
		t.Fatalf("lost partial error detail: %v", terminalErr)
	}
}
