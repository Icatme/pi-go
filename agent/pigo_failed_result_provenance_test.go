package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/pkg/pigo"
)

func TestPigoFailedGenerationResultRecovery(t *testing.T) {
	for _, gated := range []bool{false, true} {
		for _, resume := range []bool{false, true} {
			t.Run(fmt.Sprintf("gate=%v/continue=%v", gated, resume), func(t *testing.T) {
				hooks := LoopHooks{}
				gates := 0
				if gated {
					hooks.ToolGate = func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
						gates++
						return ToolGateResult{Action: ToolGateActionAllow}, nil
					}
				}
				var requests atomic.Int32
				bodies := make(chan string, 3)
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					bodies <- string(body)
					w.Header().Set("Content-Type", "text/event-stream")
					if requests.Add(1) == 1 {
						fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_partial","function":{"name":"echo","arguments":"{}"}}]}}]}

data: {"error":{"message":"fixture interrupted","type":"server_error"}}

`)
					} else {
						fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"recovered\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					}
				}))
				defer s.Close()
				model := pigo.Model{ID: "fixture", Provider: "openai", API: "openai-completions", BaseURL: s.URL}
				entered := 0
				d := AgentDefinition{Model: StreamFunc(func(ctx context.Context, r ModelRequest) (AssistantStream, error) {
					c, cancel := context.WithCancel(ctx)
					return newPigoAssistantStream(c, cancel, pigo.Stream(model, buildPigoContext(r.SystemPrompt, r.Messages, r.Tools), pigo.ProviderStreamOptions{APIKey: "fixture", RequestContext: c})), nil
				}), Tools: []ToolDefinition{{Name: "echo", Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
					entered++
					return terminatingTool(false)(ctx, in)
				}}}}
				e := NewEngine()
				auditResults := 0
				next, err := e.RunWithHooks(t.Context(), d, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, func(event AgentEvent) {
					if event.Type == EventMessageEnd && event.Message != nil && event.Message.Role == RoleTool {
						auditResults++
						if event.Message.ToolResult.Provenance != ToolResultProvenanceGenerationFailedUnexecuted {
							t.Error("event lost provenance")
						}
					}
				}, hooks)
				if err != nil {
					t.Fatal(err)
				}
				synthetic := 0
				for _, m := range next.Messages {
					if m.Role == RoleTool {
						synthetic++
						if m.ToolResult.Provenance != ToolResultProvenanceGenerationFailedUnexecuted {
							t.Fatal("unmarked synthetic")
						}
						if m.ToolResult.Execution != nil {
							t.Fatal("unexpected execution metadata")
						}
					}
				}
				<-bodies
				saved, err := json.Marshal(next)
				if err != nil {
					t.Fatal(err)
				}
				var restored AgentSnapshot
				if err = json.Unmarshal(saved, &restored); err != nil {
					t.Fatal(err)
				}
				before, _ := json.Marshal(restored)
				if resume {
					_, err = e.ContinueWithHooks(t.Context(), d, &restored, nil, hooks)
				} else {
					_, err = e.RunWithHooks(t.Context(), d, &restored, []Message{NewUserTextMessage("retry")}, nil, hooks)
				}
				if err != nil {
					t.Fatal(err)
				}
				wire := <-bodies
				t.Logf("requests=%d executed=%d synthetic=%d second=%s", requests.Load(), entered, synthetic, wire)
				after, _ := json.Marshal(restored)
				if string(before) != string(after) {
					t.Fatal("restored audit snapshot mutated")
				}
				if requests.Load() != 2 || auditResults != 1 {
					t.Fatalf("requests=%d audit=%d", requests.Load(), auditResults)
				}
				if synthetic != 1 || gates != 0 {
					t.Fatalf("synthetic=%d gates=%d", synthetic, gates)
				}
				if entered != 0 {
					t.Fatal("unexpected execution")
				}
				if strings.Contains(wire, `"role":"tool"`) {
					t.Fatal("core synthetic tool result replayed without failed assistant call")
				}
			})
		}
	}
}

func TestPigoFailedResultProvenanceProjectionBoundary(t *testing.T) {
	call := ToolCall{ID: "same", Name: "echo"}
	failed := Message{Role: RoleAssistant, StopReason: StopReasonError, ToolCalls: []ToolCall{call}}
	marked := NewToolResultMessage(call, ToolResult{Content: []Part{{Type: PartTypeText, Text: "audit"}}}, true)
	marked.ToolResult.Provenance = ToolResultProvenanceGenerationFailedUnexecuted
	for _, tc := range []struct {
		name   string
		mutate func(*Message, *Message)
		want   int
	}{
		{"marked", func(*Message, *Message) {}, 0},
		{"aborted", func(a *Message, _ *Message) { a.StopReason = StopReasonAborted }, 0},
		{"matching original", func(a *Message, r *Message) {
			a.ToolCalls[0].OriginalID = "original|id"
			r.ToolResult.OriginalToolCallID = "original|id"
		}, 0},
		{"old", func(_ *Message, r *Message) { r.ToolResult.Provenance = "" }, 1},
		{"unknown", func(_ *Message, r *Message) { r.ToolResult.Provenance = "future" }, 1},
		{"executed", func(_ *Message, r *Message) { r.ToolResult.Execution = &ToolExecutionInfo{Local: ToolLocalReturned} }, 1},
		{"not error", func(_ *Message, r *Message) { r.ToolResult.IsError = false }, 1},
		{"wrong name", func(_ *Message, r *Message) { r.ToolResult.ToolName = "other" }, 1},
		{"wrong original id", func(_ *Message, r *Message) { r.ToolResult.OriginalToolCallID = "foreign" }, 1},
		{"wrong id", func(_ *Message, r *Message) { r.ToolResult.ToolCallID = "other" }, 1},
		{"successful assistant", func(a *Message, _ *Message) { a.StopReason = StopReasonToolUse }, 1},
		{"duplicate pair", func(a *Message, _ *Message) { a.ToolCalls = append(a.ToolCalls, call) }, 1},
		{"duplicate id different name", func(a *Message, _ *Message) { a.ToolCalls = append(a.ToolCalls, ToolCall{ID: "same", Name: "other"}) }, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, r := cloneMessage(failed), cloneMessage(marked)
			tc.mutate(&a, &r)
			messages := []Message{a, r}
			before, _ := json.Marshal(messages)
			out := convertMessagesToPigo(messages)
			count := 0
			for _, m := range out {
				if _, ok := m.(pigo.ToolResultMessage); ok {
					count++
				}
			}
			if count != tc.want {
				t.Fatalf("results=%d want%d", count, tc.want)
			}
			after, _ := json.Marshal(messages)
			if string(before) != string(after) {
				t.Fatal("projection changed audit history")
			}
		})
	}
	// A later successful round reusing the same call ID must retain its result.
	success := cloneMessage(failed)
	success.StopReason = StopReasonToolUse
	for _, boundary := range []Message{success, NewUserTextMessage("new round")} {
		out := convertMessagesToPigo([]Message{failed, marked, boundary, marked})
		count := 0
		for _, m := range out {
			if _, ok := m.(pigo.ToolResultMessage); ok {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("cross-round marker suppressed result: %+v", out)
		}
	}
	clone := cloneMessage(marked)
	clone.ToolResult.Provenance = "changed"
	if marked.ToolResult.Provenance != ToolResultProvenanceGenerationFailedUnexecuted {
		t.Fatal("clone pointer alias")
	}
}
