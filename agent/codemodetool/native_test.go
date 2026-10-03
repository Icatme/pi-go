package codemodetool

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
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/codemode"
	"github.com/Icatme/pi-go/pkg/pigo"
)

func nativeSSE(events ...map[string]any) string {
	var out strings.Builder
	for _, event := range events {
		encoded, _ := json.Marshal(event)
		fmt.Fprintf(&out, "data: %s\n\n", encoded)
	}
	return out.String()
}

func TestNativeCodemodeAgentRoundTrip(t *testing.T) {
	for _, scenario := range []string{"success", "tool error", "partial", "cancel", "source bound", "old function call", "function fallback", "dynamic"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			var executed, requests atomic.Int32
			leaf := agent.ToolDefinition{Name: "echo", Parameters: map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}, "required": []string{"value"}},
				Execute: func(_ context.Context, execution agent.ToolExecutionContext) (agent.ToolResult, error) {
					executed.Add(1)
					if scenario == "tool error" {
						return agent.ToolResult{}, errors.New("fixture leaf failure")
					}
					value := execution.Args.(map[string]any)["value"].(string)
					return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: value}}}, nil
				},
			}
			config := &pigo.ToolConstrainedSampling{Type: "grammar", Syntax: "lark", Definition: "start: /[\\s\\S]+/", Mode: "prefer"}
			options := Options{ConstrainedSampling: config}
			sandbox := newSandbox(t)
			bindings := []Binding{Native(leaf, "fixture")}
			tool, err := New(sandbox, bindings, options)
			if scenario == "dynamic" {
				tool, err = NewDynamic(sandbox, func(context.Context) ([]Binding, []codemode.Namespace, error) { return bindings, nil, nil }, options)
			}
			if err != nil {
				t.Fatal(err)
			}
			config.Definition = "mutated after creation"
			if tool.ConstrainedSampling.Definition == config.Definition {
				t.Fatal("sampling options aliased")
			}
			if scenario == "old function call" {
				tool.ConstrainedSampling = nil
			}
			source := "text(await tools.echo({value: \"雪\\n\\\"quoted\\\"\"}));"
			if scenario == "source bound" {
				source = strings.Repeat(" ", (64<<10)+1)
			}
			custom := scenario != "function fallback" && scenario != "old function call"
			functionResponse := !custom || scenario == "old function call"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				phase := requests.Add(1)
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				tools := body["tools"].([]any)
				wantType := "function"
				if custom {
					wantType = "custom"
				}
				if len(tools) != 1 || tools[0].(map[string]any)["type"] != wantType {
					t.Errorf("tools=%v", tools)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if phase > 1 {
					var call, output map[string]any
					for _, raw := range body["input"].([]any) {
						item := raw.(map[string]any)
						if item["name"] == "code" {
							call = item
						}
						if item["call_id"] == "call_code" && item["output"] != nil {
							output = item
						}
					}
					kind := "function_call"
					if custom {
						kind = "custom_tool_call"
					}
					if call == nil || output == nil || call["type"] != kind || output["type"] != kind+"_output" {
						t.Errorf("call=%v output=%v", call, output)
					}
					if custom && call["input"] != source {
						t.Errorf("source was changed: %v", call)
					}
					if custom && !functionResponse && call["id"] != "ctc_code" {
						t.Errorf("lost custom item ID: %v", call)
					}
					if scenario == "success" && !strings.Contains(fmt.Sprint(output["output"]), "雪") {
						t.Errorf("lost tool output: %v", output)
					}
					fmt.Fprint(w, nativeSSE(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "id": "msg_done", "content": []any{map[string]any{"type": "output_text", "text": "done"}}}}}}))
					return
				}
				item := map[string]any{"type": "custom_tool_call", "id": "ctc_code", "call_id": "call_code", "name": "code", "input": source, "status": "completed"}
				if functionResponse {
					args, _ := json.Marshal(map[string]any{"code": source})
					item["type"], item["id"], item["arguments"] = "function_call", "fc_code", string(args)
					delete(item, "input")
				}
				if scenario == "partial" || scenario == "cancel" {
					fmt.Fprint(w, nativeSSE(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item}))
					w.(http.Flusher).Flush()
					if scenario == "cancel" {
						<-r.Context().Done()
					}
					return
				}
				fmt.Fprint(w, nativeSSE(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{item}}}))
			}))
			defer server.Close()
			modelID := "gpt-6-astra"
			if scenario == "function fallback" {
				modelID = "gpt-5.4"
			}
			definition := agent.AgentDefinition{DefaultModel: agent.ModelRef{Provider: "openai", Model: modelID, ProviderConfig: agent.ProviderConfig{APIKey: "fixture", BaseURL: server.URL}}, Tools: []agent.ToolDefinition{tool}, MaxTurns: 2,
				OnProviderStreamEvent: func(raw json.RawMessage, _ agent.ModelRef) error {
					if scenario == "cancel" && strings.Contains(string(raw), "response.output_item.added") {
						cancel()
					}
					return nil
				},
			}
			runner, err := agent.NewRunner(definition)
			if err != nil {
				t.Fatal(err)
			}
			stream := runner.Query(ctx, "run fixture")
			for range stream.Events() {
			}
			snapshot, runErr := stream.Wait()
			if scenario == "partial" || scenario == "cancel" {
				if executed.Load() != 0 || requests.Load() != 1 {
					t.Fatalf("partial execution=%d requests=%d err=%v", executed.Load(), requests.Load(), runErr)
				}
				return
			}
			if runErr != nil {
				t.Fatal(runErr)
			}
			wantExecuted := int32(1)
			if scenario == "source bound" {
				wantExecuted = 0
			}
			if executed.Load() != wantExecuted || requests.Load() != 2 {
				t.Fatalf("executions=%d requests=%d", executed.Load(), requests.Load())
			}
			var result *agent.ToolResultPayload
			for _, message := range snapshot.Messages {
				if message.Role == agent.RoleTool {
					result = message.ToolResult
				}
			}
			wantError := scenario == "tool error" || scenario == "source bound"
			if result == nil || result.IsError != wantError {
				t.Fatalf("tool result=%+v", result)
			}
			declarations := agent.GetCurrentTools(snapshot.Messages)
			if scenario == "old function call" {
				return
			}
			if len(declarations) != 1 || declarations[0].ConstrainedSampling == nil || declarations[0].ConstrainedSampling.Definition == "mutated after creation" {
				t.Fatalf("lost sampling declaration: %+v", declarations)
			}
		})
	}
}
