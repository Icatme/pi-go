package pigo

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestOpenCodeGoKimiK27ThinkingWire(t *testing.T) {
	for _, level := range []ThinkingLevel{"", ThinkingLevelMedium, ThinkingLevelHigh} {
		t.Run(string(level), func(t *testing.T) {
			var captured map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
					t.Errorf("decode request: %v", err)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"id\":\"kimi\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"plan\"},\"finish_reason\":null}]}\n\n"))
				_, _ = w.Write([]byte("data: {\"id\":\"kimi\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
			}))
			defer server.Close()
			model := GetModel("opencode-go", "kimi-k2.7-code")
			if model == nil {
				t.Fatal("missing Kimi K2.7 Code")
			}
			if slices.Contains(GetSupportedThinkingLevels(*model), ModelThinkingLevelOff) {
				t.Error("always-thinking Kimi must not advertise off")
			}
			model.BaseURL = server.URL + "/v1"
			result := Complete(*model, Context{Messages: []Message{UserMessage{Content: "reply"}}}, BuildProviderStreamOptions(*model, SimpleStreamOptions{APIKey: "fixture", Reasoning: level}))
			if result.StopReason != StopReasonStop {
				t.Fatalf("unexpected result: %+v", result)
			}
			if _, exists := captured["reasoning_effort"]; exists {
				t.Errorf("K2.7 does not accept reasoning_effort: %#v", captured)
			}
			if level == "" {
				if _, exists := captured["thinking"]; exists {
					t.Errorf("default must omit the optional control, not disable thinking: %#v", captured["thinking"])
				}
			} else if thinking, ok := captured["thinking"].(map[string]any); !ok || thinking["type"] != "enabled" {
				t.Errorf("expected enabled thinking object: %#v", captured["thinking"])
			}
			if len(result.Content) != 2 {
				t.Fatalf("unexpected content: %#v", result.Content)
			}
			if thought, ok := result.Content[0].(ThinkingContent); !ok || thought.Thinking != "plan" {
				t.Errorf("reasoning_content must remain a thinking block: %#v", result.Content[0])
			}
		})
	}
}
