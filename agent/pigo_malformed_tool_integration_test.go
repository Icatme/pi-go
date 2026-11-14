package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/pkg/pigo"
)

func TestPigoMalformedChatToolBatchDoesNotPoisonAgent(t *testing.T) {
	for _, finish := range []string{"tool_calls", "length"} {
		for _, blankID := range []bool{false, true} {
			for _, gated := range []bool{false, true} {
				t.Run(fmt.Sprintf("finish=%s/blankID=%v/gated=%v", finish, blankID, gated), func(t *testing.T) {
					var requests atomic.Int32
					bodies := make(chan string, 4)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						var body json.RawMessage
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
							w.WriteHeader(400)
							return
						}
						bodies <- string(body)
						n := requests.Add(1)
						w.Header().Set("Content-Type", "text/event-stream")
						if n == 1 {
							id, name := "bad_call", ""
							if blankID {
								id, name = "", "echo"
							}
							fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"working\",\"tool_calls\":[{\"index\":0,\"id\":\"valid_call\",\"type\":\"function\",\"function\":{\"name\":\"echo\",\"arguments\":\"{}\"}},{\"index\":1,\"id\":%q,\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7}}\n\ndata: [DONE]\n\n", id, name, finish)
						} else {
							fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"recovered\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
						}
					}))
					defer server.Close()
					model := pigo.Model{ID: "fixture", API: "openai-completions", Provider: "openai", BaseURL: server.URL, MaxTokens: 1024}
					entered, gates := 0, 0
					definition := AgentDefinition{Model: StreamFunc(func(ctx context.Context, r ModelRequest) (AssistantStream, error) {
						c, cancel := context.WithCancel(ctx)
						stream := pigo.Stream(model, buildPigoContext(r.SystemPrompt, r.Messages, r.Tools), pigo.ProviderStreamOptions{APIKey: "fixture", RequestContext: c, MaxRetries: 2})
						return newPigoAssistantStream(c, cancel, stream), nil
					}), Tools: []ToolDefinition{{Name: "echo", Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
						entered++
						return terminatingTool(false)(ctx, in)
					}}}}
					hooks := LoopHooks{}
					if gated {
						hooks.ToolGate = func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
							gates++
							return ToolGateResult{Action: ToolGateActionAllow}, nil
						}
					}
					engine := NewEngine()
					next, err := engine.RunWithHooks(t.Context(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil, hooks)
					if err != nil {
						t.Fatal(err)
					}
					if requests.Load() != 1 || entered != 0 || gates != 0 {
						t.Fatalf("requests=%d tool=%d gate=%d", requests.Load(), entered, gates)
					}
					last := next.Messages[len(next.Messages)-1]
					if last.StopReason != StopReasonError || len(last.ToolCalls) != 0 {
						t.Fatalf("malformed batch retained: %+v", last)
					}
					for _, m := range next.Messages {
						if m.Role == RoleTool {
							t.Fatalf("orphan tool result: %+v", m)
						}
					}
					<-bodies
					// Existing Continue rejects assistant tails; a new prompt may recover safely.
					_, err = engine.ContinueWithHooks(t.Context(), definition, next, nil, hooks)
					if !errors.Is(err, ErrCannotContinueFromAssistant) || requests.Load() != 1 {
						t.Fatalf("assistant-tail continuation was not rejected without HTTP: %v", err)
					}
					next, err = engine.RunWithHooks(t.Context(), definition, next, []Message{NewUserTextMessage("try again")}, nil, hooks)
					if err != nil {
						t.Fatal(err)
					}
					body := <-bodies
					if requests.Load() != 2 || strings.Contains(body, "valid_call") || strings.Contains(body, "bad_call") || strings.Contains(body, `"role":"tool"`) {
						t.Fatalf("poisoned continuation: %s", body)
					}
					if next.Messages[len(next.Messages)-1].StopReason != StopReasonStop {
						t.Fatalf("did not recover: %+v", next)
					}
				})
			}
		}

	}
}

// Complete identity with truncated arguments still feeds a tool error back to
// the model. The malformed-identity guard must not disable Length recovery.
func TestPigoChatLengthWithCompleteIdentityRecovers(t *testing.T) {
	for _, gated := range []bool{false, true} {
		t.Run(fmt.Sprintf("gated=%v", gated), func(t *testing.T) {
			var requests atomic.Int32
			bodies := make(chan string, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				bodies <- string(body)
				w.Header().Set("Content-Type", "text/event-stream")
				if requests.Add(1) == 1 {
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"length_call\",\"function\":{\"name\":\"echo\",\"arguments\":\"{\"}}]},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n")
				} else {
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"recovered\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				}
			}))
			defer server.Close()
			model := pigo.Model{ID: "fixture", API: "openai-completions", Provider: "openai", BaseURL: server.URL, MaxTokens: 1024}
			entered, gates := 0, 0
			definition := AgentDefinition{Model: StreamFunc(func(ctx context.Context, r ModelRequest) (AssistantStream, error) {
				c, cancel := context.WithCancel(ctx)
				return newPigoAssistantStream(c, cancel, pigo.Stream(model, buildPigoContext(r.SystemPrompt, r.Messages, r.Tools), pigo.ProviderStreamOptions{APIKey: "fixture", RequestContext: c})), nil
			}), Tools: []ToolDefinition{{Name: "echo", Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
				entered++
				return terminatingTool(false)(ctx, in)
			}}}}
			hooks := LoopHooks{}
			if gated {
				hooks.ToolGate = func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
					gates++
					return ToolGateResult{Action: ToolGateActionAllow}, nil
				}
			}
			next, err := NewEngine().RunWithHooks(t.Context(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil, hooks)
			if err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 2 || entered != 0 || gates != 0 {
				t.Fatalf("requests=%d entered=%d gates=%d", requests.Load(), entered, gates)
			}
			<-bodies
			wire := <-bodies
			if !strings.Contains(wire, `"tool_call_id":"length_call"`) || !strings.Contains(wire, "model response was truncated") {
				t.Fatalf("missing normal Length feedback: %s", wire)
			}
			var retained bool
			for _, m := range next.Messages {
				if m.Role == RoleAssistant && m.StopReason == StopReasonLength {
					retained = len(m.ToolCalls) == 1
				}
			}
			if !retained || next.Messages[len(next.Messages)-1].StopReason != StopReasonStop {
				t.Fatalf("Length recovery changed: %+v", next.Messages)
			}
		})
	}
}
