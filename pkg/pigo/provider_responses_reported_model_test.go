package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResponsesReportedModelDiagnostic(t *testing.T) {
	for _, api := range []string{"openai-responses", "openai-codex-responses"} {
		for _, terminalType := range []string{"completed", "done", "incomplete", "failed"} {
			for _, tc := range []struct{ name, created, terminal, want string }{
				{"different", "first-reported", "final-reported", "final-reported"},
				{"terminal-only", "", "final-reported", "final-reported"},
				{"empty-terminal", "first-reported", "", "first-reported"},
				{"missing-terminal", "first-reported", "", "first-reported"},
				{"same-terminal", "first-reported", "requested", "first-reported"},
				{"explicit-empty", "", "", ""},
				{"same", "requested", "requested", ""},
				{"missing", "", "", ""},
			} {
				t.Run(api+"/"+terminalType+"/"+tc.name, func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						var body struct {
							Model string `json:"model"`
						}
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
							return
						}
						if body.Model != "requested" {
							t.Errorf("request model=%q", body.Model)
						}
						w.Header().Set("Content-Type", "text/event-stream")
						created := map[string]any{"id": "resp_diag"}
						if tc.created != "" || tc.name == "explicit-empty" {
							created["model"] = tc.created
						}
						status := terminalType
						if status == "done" {
							status = "completed"
						}
						terminal := map[string]any{"id": "resp_diag", "status": status, "usage": map[string]any{"input_tokens": 3, "output_tokens": 2, "total_tokens": 5}}
						if tc.terminal != "" || tc.name == "empty-terminal" || tc.name == "explicit-empty" {
							terminal["model"] = tc.terminal
						}
						if terminalType == "failed" {
							terminal["error"] = map[string]any{"code": "server_error", "message": "fixture failure"}
						}
						for _, e := range []map[string]any{
							{"type": "response.created", "response": created},
							{"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "delta": "answer"},
							{"type": "response." + terminalType, "response": terminal},
						} {
							b, _ := json.Marshal(e)
							fmt.Fprintf(w, "data: %s\n\n", b)
							w.(http.Flusher).Flush()
						}
					}))
					defer server.Close()
					m := Model{ID: "requested", Provider: "fixture", API: API(api), BaseURL: server.URL, MaxTokens: 64, Cost: UsageCost{Input: 1000000, Output: 2000000}}
					key := "fixture"
					if api == "openai-codex-responses" {
						key = makeOpenAICodexToken("fixture-account")
					}
					stream := Stream(m, Context{}, ProviderStreamOptions{APIKey: key, Transport: TransportSSE, MaxRetries: 2})
					events := collectAssistantMessageEvents(stream.Events())
					result := stream.Result()
					if result.Model != m.ID || result.Provider != m.Provider || result.API != m.API || result.ResponseModel != tc.want || result.ResponseID != "resp_diag" {
						t.Fatalf("result=%+v want=%q", result, tc.want)
					}
					wantStop := StopReasonStop
					if terminalType == "incomplete" {
						wantStop = StopReasonLength
					}
					if terminalType == "failed" {
						wantStop = StopReasonError
					}
					if result.StopReason != wantStop || result.Usage.Input != 3 || result.Usage.Output != 2 {
						t.Fatalf("terminal=%+v", result)
					}
					if result.Usage.Cost.Input != 3 || result.Usage.Cost.Output != 4 || result.Usage.Cost.Total != 7 {
						t.Fatalf("pricing changed: %+v", result.Usage.Cost)
					}
					wantPartial := tc.created
					if wantPartial == m.ID {
						wantPartial = ""
					}
					seen := false
					for _, e := range events {
						if e.Type == AssistantMessageEventTextDelta {
							seen = true
							if e.Partial.ResponseModel != wantPartial || e.Partial.Model != m.ID {
								t.Fatalf("partial mutated=%+v want=%q", e.Partial, wantPartial)
							}
						}
					}
					if !seen {
						t.Fatal("no text delta")
					}
				})
			}
		}
	}
}
