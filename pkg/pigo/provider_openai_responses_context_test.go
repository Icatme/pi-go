package pigo

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"
)

func TestResponsesRequestContextStages(t *testing.T) {
	const update = "Updated system prompt section \"policy\":\n\nnew policy\n\nRemoved system prompt section \"removed\"."
	declarations := func(names ...string) []map[string]any {
		var result []map[string]any
		for _, name := range names {
			result = append(result, map[string]any{
				"type": "function", "name": name, "description": "Synthetic local lookup", "strict": false,
				"parameters": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
			})
		}
		return result
	}
	for _, test := range []struct {
		name         string
		api          API
		native       bool
		instructions string
		systemText   string
		anchor       bool
	}{
		{name: "responses/native", api: "openai-responses", native: true, systemText: update, anchor: true},
		{name: "responses/collapsed", api: "openai-responses", systemText: "new policy"},
		{name: "codex/native", api: "openai-codex-responses", native: true, instructions: "You are a helpful assistant.", systemText: update},
		{name: "codex/collapsed", api: "openai-codex-responses", instructions: "new policy"},
	} {
		for _, initialTools := range []bool{false, true} {
			for _, stop := range []StopReason{StopReasonError, StopReasonAborted} {
				t.Run(fmt.Sprintf("%s/initial_tools=%t/%s", test.name, initialTools, stop), func(t *testing.T) {
					model := *GetModel("openai", "gpt-6-astra")
					model.API = test.api
					model.Compat = &OpenAIResponsesCompat{SupportsMidConvoSystemMessages: &test.native}
					ctx := Context{Messages: []Message{
						AssistantMessage{StopReason: stop, Content: []ContentBlock{TextContent{Text: "failed attempt"}}},
						SystemMessage{
							Sections:   map[string]*string{"policy": transcriptString("new policy"), "removed": nil},
							ToolsAdded: []Tool{anchorFunction("zebra"), anchorFunction("alpha")},
						},
						UserMessage{Content: "continue"},
					}}
					var wantNames []string
					if initialTools {
						ctx.Tools = []Tool{anchorFunction("initial")}
						wantNames = append(wantNames, "initial")
					}
					before, err := SerializeContext(ctx)
					if err != nil {
						t.Fatal(err)
					}
					var request openAIResponsesRequest
					if test.api == "openai-codex-responses" {
						request = buildOpenAICodexRequest(nil, model, ctx, ProviderStreamOptions{})
					} else {
						request = mustBuildOpenAIResponsesRequest(t, nil, model, ctx, ProviderStreamOptions{})
					}
					var wantInput []map[string]any
					if test.anchor {
						wantInput = append(wantInput, map[string]any{
							"type": "additional_tools", "role": "developer", "tools": declarations("zebra", "alpha"),
						})
					} else {
						wantNames = append(wantNames, "zebra", "alpha")
					}
					if test.systemText != "" {
						wantInput = append(wantInput, map[string]any{"role": "developer", "content": test.systemText})
					}
					wantInput = append(wantInput, map[string]any{
						"role": "user", "content": []map[string]any{{"type": "input_text", "text": "continue"}},
					})
					if request.Instructions != test.instructions {
						t.Fatalf("instructions = %q, want %q", request.Instructions, test.instructions)
					}
					if !reflect.DeepEqual(request.Input, wantInput) {
						t.Fatalf("input = %#v, want %#v", request.Input, wantInput)
					}
					if wantTools := declarations(wantNames...); !reflect.DeepEqual(request.Tools, wantTools) {
						t.Fatalf("tools = %#v, want %#v", request.Tools, wantTools)
					}
					after, err := SerializeContext(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(before, after) {
						t.Fatalf("request builder mutated caller context:\nbefore: %s\nafter: %s", before, after)
					}
				})
			}
		}
	}
}
