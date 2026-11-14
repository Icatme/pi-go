package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestXAIEmptyToolsDefaultChoice(t *testing.T) {
	for _, tc := range []struct {
		name             string
		provider         Provider
		choice, previous string
		tools            bool
		want             string
	}{
		{name: "xai default", provider: "xai"},
		{name: "xai explicit auto", provider: "xai", choice: "auto", want: "auto"},
		{name: "xai explicit none", provider: "xai", choice: "none", want: "none"},
		{name: "xai explicit required", provider: "xai", choice: "required", want: "required"},
		{name: "xai tools", provider: "xai", tools: true, want: "auto"},
		{name: "xai inherited", provider: "xai", previous: "resp_prior", want: "auto"},
		{name: "openai unchanged", provider: "openai", want: "auto"},
		{name: "custom unchanged", provider: "custom", want: "auto"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wire map[string]json.RawMessage
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if e := json.NewDecoder(r.Body).Decode(&wire); e != nil {
					t.Error(e)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
			}))
			defer server.Close()
			ctx := Context{Messages: []Message{UserMessage{Content: "hello"}}}
			if tc.tools {
				ctx.Tools = []Tool{{Name: "lookup", Parameters: map[string]any{"type": "object"}}}
			}
			result := Complete(Model{ID: "fixture", Provider: tc.provider, API: "openai-responses", BaseURL: server.URL}, ctx, ProviderStreamOptions{APIKey: "fixture", ToolChoice: tc.choice, PreviousResponseID: tc.previous})
			if result.StopReason != StopReasonStop || requests != 1 {
				t.Fatalf("requests=%d response=%+v", requests, result)
			}
			raw, exists := wire["tool_choice"]
			if tc.want == "" {
				if exists {
					t.Fatalf("invented default choice: %s", raw)
				}
			} else {
				var value string
				if json.Unmarshal(raw, &value) != nil || value != tc.want {
					t.Fatalf("choice changed: %s", raw)
				}
			}
			_, hasTools := wire["tools"]
			if hasTools != tc.tools {
				t.Fatalf("tools presence changed: %+v", wire)
			}
		})
	}
}
