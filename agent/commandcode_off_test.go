package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAgentCommandCodeThinkingOffOnTheWire(t *testing.T) {
	for _, modelID := range []string{"claude-fable-5", "gpt-5.6-luna"} {
		for _, transport := range []string{"native", "generate"} {
			for _, preference := range []ThinkingLevel{"", ThinkingOff} {
				name := "default"
				if preference == ThinkingOff {
					name = "explicit"
				}
				t.Run(modelID+"/"+transport+"/"+name, func(t *testing.T) {
					type capturedRequest struct {
						path    string
						payload map[string]any
					}
					requests := make(chan capturedRequest, 4)
					nativePath := "/provider/v1/chat/completions"
					if modelID == "claude-fable-5" {
						nativePath = "/provider/v1/messages"
					}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						var payload map[string]any
						if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
							t.Error(err)
						}
						requests <- capturedRequest{path: r.URL.Path, payload: payload}
						if transport == "generate" && r.URL.Path == nativePath {
							w.WriteHeader(http.StatusForbidden)
							fmt.Fprint(w, `{"error":{"code":"upgrade_required"}}`)
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						switch r.URL.Path {
						case "/alpha/generate":
							fmt.Fprintln(w, `{"type":"text-delta","text":"ok"}`)
							fmt.Fprintln(w, `{"type":"finish","finishReason":"stop"}`)
						case "/provider/v1/messages":
							fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_off\",\"model\":\"claude-fable-5\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
							fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
							fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
						case "/provider/v1/chat/completions":
							fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
						default:
							t.Errorf("unexpected request path %s", r.URL.Path)
							w.WriteHeader(http.StatusNotFound)
						}
					}))
					defer server.Close()
					runtime, err := NewAgent(AgentDefinition{
						DefaultModel:  ModelRef{Provider: "commandcode", Model: modelID, ProviderConfig: ProviderConfig{BaseURL: server.URL, APIKey: t.Name()}},
						ThinkingLevel: preference,
					})
					if err != nil {
						t.Fatal(err)
					}
					if err := runtime.PromptText(context.Background(), "run"); err != nil {
						t.Fatal(err)
					}
					state := runtime.State()
					if state.Error != "" {
						t.Fatal(state.Error)
					}
					if state.RequestedThinkingLevel != ThinkingOff || state.ThinkingLevel != ThinkingOff {
						t.Errorf("off changed: requested=%q effective=%q", state.RequestedThinkingLevel, state.ThinkingLevel)
					}
					wantRequests := 1
					if transport == "generate" {
						wantRequests = 2
					}
					if len(requests) != wantRequests {
						t.Fatalf("requests=%d want %d", len(requests), wantRequests)
					}
					for index := 0; index < wantRequests; index++ {
						request := <-requests
						wantPath := nativePath
						if index == 1 {
							wantPath = "/alpha/generate"
						}
						if request.path != wantPath {
							t.Errorf("request path=%q want %q", request.path, wantPath)
						}
						payload := request.payload
						if request.path == "/alpha/generate" {
							payload, _ = payload["params"].(map[string]any)
						}
						if _, exists := payload["reasoning_effort"]; exists {
							t.Errorf("off sent reasoning effort to %s: %+v", request.path, payload)
						}
						if _, exists := payload["output_config"]; exists {
							t.Errorf("off sent adaptive effort to %s: %+v", request.path, payload)
						}
						if request.path == "/provider/v1/messages" {
							thinking, _ := payload["thinking"].(map[string]any)
							if thinking["type"] != "disabled" {
								t.Errorf("off enabled Anthropic thinking: %+v", thinking)
							}
						}
					}
				})
			}
		}
	}
}
