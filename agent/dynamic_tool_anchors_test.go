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
)

// Exercise the existing resolver and durable declaration messages through the
// actual provider adapter, execution loop and restored Agent snapshot.
func TestAgentNativeDynamicToolAnchorsAndRestoration(t *testing.T) {
	for _, mode := range []string{"native", "unsupported model", "remove", "redefine", "foreign namespace"} {
		t.Run(mode, func(t *testing.T) {
			var requests, loaded, executed atomic.Int32
			var lastBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				phase := requests.Add(1)
				if err := json.NewDecoder(r.Body).Decode(&lastBody); err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				item := map[string]any{"type": "message", "id": "msg_done", "content": []any{map[string]any{"type": "output_text", "text": "done"}}}
				if phase <= 2 {
					name, namespace := "load", ""
					if phase == 2 {
						name = "lookup"
						if mode == "native" {
							namespace = "lookup"
						}
						if mode == "foreign namespace" {
							namespace = "another_surface"
						}
					}
					item = map[string]any{"type": "function_call", "id": "fc_" + name, "call_id": "call_" + name, "name": name, "namespace": namespace, "arguments": "{}", "status": "completed"}
				}
				fmt.Fprint(w, buildCodexSSE(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{item}}}))
			}))
			defer server.Close()
			model := "gpt-6-astra"
			if mode == "unsupported model" {
				model = "gpt-5.4"
			}
			loader := ToolDefinition{Name: "load", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}, Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
				loaded.Add(1)
				return ToolResult{Content: []Part{{Type: PartTypeText, Text: "lookup is now available"}}}, nil
			}}
			lookup := ToolDefinition{Name: "lookup", Description: "first revision", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}, Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
				executed.Add(1)
				return ToolResult{Content: []Part{{Type: PartTypeText, Text: "bounded local result"}}}, nil
			}}
			definition := AgentDefinition{SystemPrompt: "synthetic local fixture", Tools: []ToolDefinition{loader}, DefaultModel: ModelRef{Provider: "openai", Model: model, ProviderConfig: ProviderConfig{APIKey: "fixture", BaseURL: server.URL}}, ToolResolver: func(_ context.Context, _ AgentSnapshot) ([]ToolDefinition, error) {
				tools := []ToolDefinition{loader}
				if loaded.Load() > 0 {
					if mode == "remove" {
						tools = nil
					}
					current := lookup
					if mode == "redefine" && executed.Load() > 0 {
						current.Description = "second revision"
					}
					tools = append(tools, current)
				}
				return tools, nil
			}}
			next, err := NewEngine().Run(t.Context(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("load then lookup")}, nil)
			if mode == "foreign namespace" {
				if executed.Load() != 0 || loaded.Load() != 1 || requests.Load() != 2 || !strings.Contains(next.Error, "unsupported namespace") {
					t.Fatalf("foreign namespace executed: requests=%d executed=%d err=%v", requests.Load(), executed.Load(), err)
				}
				return
			}
			if err != nil || loaded.Load() != 1 || executed.Load() != 1 || requests.Load() != 3 {
				debug, _ := json.Marshal(next)
				t.Fatalf("requests=%d loaded=%d executed=%d err=%v snapshot=%s", requests.Load(), loaded.Load(), executed.Load(), err, debug)
			}
			wire, err := json.Marshal(next)
			if err != nil {
				t.Fatal(err)
			}
			var restored AgentSnapshot
			if err := json.Unmarshal(wire, &restored); err != nil {
				t.Fatal(err)
			}
			_, err = NewEngine().Run(t.Context(), definition, &restored, []Message{NewUserTextMessage("resume without repeating execution")}, nil)
			if err != nil || loaded.Load() != 1 || executed.Load() != 1 || requests.Load() != 4 {
				t.Fatalf("restore repeated execution or failed: %v", err)
			}
			encoded, _ := json.Marshal(lastBody)
			wantAnchor := mode == "native"
			if strings.Contains(string(encoded), `"additional_tools"`) != wantAnchor {
				t.Fatalf("mode=%s request=%s", mode, encoded)
			}
			var anchorIndex, loadOutputIndex, lookupCallIndex int = -1, -1, -1
			for i, raw := range lastBody["input"].([]any) {
				item := raw.(map[string]any)
				if item["type"] == "additional_tools" {
					anchorIndex = i
				}
				if item["type"] == "function_call_output" && item["call_id"] == "call_load" {
					loadOutputIndex = i
				}
				if item["type"] == "function_call" && item["name"] == "lookup" {
					lookupCallIndex = i
					if (item["namespace"] == "lookup") != wantAnchor {
						t.Fatalf("namespace was lost or leaked: %v", item)
					}
				}
			}
			if wantAnchor && !(loadOutputIndex < anchorIndex && anchorIndex < lookupCallIndex) {
				t.Fatalf("anchor moved across calls: output=%d anchor=%d call=%d", loadOutputIndex, anchorIndex, lookupCallIndex)
			}
			if mode == "remove" {
				for _, raw := range lastBody["tools"].([]any) {
					if raw.(map[string]any)["name"] == "load" {
						t.Fatal("removed tool still declared")
					}
				}
			}
			if mode == "redefine" && !strings.Contains(string(encoded), "second revision") {
				t.Fatal("changed definition not sent")
			}
		})
	}
}
