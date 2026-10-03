package pigo

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func transcriptString(text string) *string { return &text }

func TestTranscriptHelpersAcceptSystemMessagePointers(t *testing.T) {
	initial := CreateInitialSystemMessage("policy", []Tool{{Name: "old", Parameters: map[string]any{"type": "object"}}})
	initial.Sections = map[string]*string{"policy": transcriptString("old section")}
	later := &SystemMessage{
		Content: "later", Sections: map[string]*string{"policy": transcriptString("new section")},
		ToolsRemoved: []ToolReference{{Name: "old"}}, ToolsAdded: []Tool{{Name: "new"}},
	}
	messages := []Message{initial, UserMessage{Content: "question"}, later, SystemMessage{Content: "value"}}
	if got := GetCurrentSystemPrompt(messages); got != "policy\n\nlater\n\nvalue\n\nnew section" {
		t.Fatalf("pointer system messages were ignored: %q", got)
	}
	if tools := GetCurrentTools(messages); len(tools) != 1 || tools[0].Name != "new" {
		t.Fatalf("pointer tool changes were ignored: %+v", tools)
	}
	head := GetInitialSystemMessage(messages)
	if head == nil || head.Content != "policy" {
		t.Fatalf("pointer initial system was ignored: %+v", head)
	}
	*head.Sections["policy"] = "mutation"
	head.ToolsAdded[0].Parameters.(map[string]any)["type"] = "mutation"
	if *initial.Sections["policy"] != "old section" || initial.ToolsAdded[0].Parameters.(map[string]any)["type"] != "object" {
		t.Fatal("pointer initial system snapshot aliases its source")
	}
	withoutInitial := WithoutInitialSystemMessage(messages)
	if len(withoutInitial) != 3 || withoutInitial[0].(UserMessage).Content != "question" {
		t.Fatalf("pointer system head was not dropped: %+v", withoutInitial)
	}
	for _, native := range []bool{false, true} {
		resolved := ResolveTranscript(TranscriptContext{Messages: messages}, native)
		wantLength := 2
		if native {
			wantLength = 4
		}
		if len(resolved.Messages) != wantLength || GetCurrentSystemPrompt(resolved.Messages) != GetCurrentSystemPrompt(messages) {
			t.Fatalf("native=%t pointer resolution changed system state: %+v", native, resolved)
		}
	}
}

func TestTranscriptNormalizeAndClone(t *testing.T) {
	if initial := CreateInitialSystemMessage("", nil); initial != nil {
		t.Fatalf("empty context created system message: %+v", initial)
	}
	if normalized := NormalizeContext(Context{}); len(normalized.Messages) != 0 {
		t.Fatalf("empty context became nonempty: %+v", normalized)
	}
	rawSchema := json.RawMessage(`{"type":"object"}`)
	tool := Tool{Name: "initial", Parameters: map[string]any{"nested": []any{"original"}}, OutputSchema: rawSchema}
	ctx := Context{SystemPrompt: "base", Tools: []Tool{tool}, Messages: []Message{
		UserMessage{Content: "first"},
		SystemMessage{Content: "later", Sections: map[string]*string{"named": transcriptString("original")}, ToolsAdded: []Tool{tool}},
	}}
	normalized := NormalizeContext(ctx)
	if len(normalized.Messages) != 3 || normalized.Messages[0].(SystemMessage).Content != "base" || !normalized.Messages[0].(SystemMessage).Timestamp.Equal(time.Unix(0, 0)) {
		t.Fatalf("authoring fields were not folded: %+v", normalized)
	}
	initial := normalized.Messages[0].(SystemMessage)
	initial.ToolsAdded[0].Parameters.(map[string]any)["nested"].([]any)[0] = "changed"
	initial.ToolsAdded[0].OutputSchema.(json.RawMessage)[0] = '['
	later := normalized.Messages[2].(SystemMessage)
	*later.Sections["named"] = "changed"
	if tool.Parameters.(map[string]any)["nested"].([]any)[0] != "original" || string(rawSchema) != `{"type":"object"}` || *ctx.Messages[1].(SystemMessage).Sections["named"] != "original" {
		t.Fatal("normalization mutated authoring context")
	}
}

func TestTranscriptReplayPromptSectionsAndToolOrder(t *testing.T) {
	timestamp := time.Unix(42, 0)
	messages := []Message{
		SystemMessage{Content: "base", Timestamp: timestamp, Sections: map[string]*string{"z": transcriptString("old"), "a": transcriptString("remove")}, ToolsAdded: []Tool{{Name: "a"}, {Name: "b"}}},
		UserMessage{Content: "first"},
		SystemMessage{Content: "extra", Sections: map[string]*string{"z": transcriptString("new"), "a": nil, "b": transcriptString("second")}, ToolsAdded: []Tool{{Name: "a", Description: "changed"}}},
		SystemMessage{ToolsRemoved: []ToolReference{{Name: "a"}}, ToolsAdded: []Tool{{Name: "c"}}},
		SystemMessage{ToolsAdded: []Tool{{Name: "a"}}},
	}
	current := GetCurrentSystemMessage(messages)
	if current == nil || current.Content != "base\n\nextra" || GetCurrentSystemPrompt(messages) != "base\n\nextra\n\nsecond\n\nnew" || !current.Timestamp.Equal(timestamp) {
		t.Fatalf("system replay mismatch: %+v", current)
	}
	if _, present := current.Sections["a"]; present {
		t.Fatal("null section patch was not removed")
	}
	names := []string{}
	for _, tool := range GetCurrentTools(messages) {
		names = append(names, tool.Name)
	}
	if !reflect.DeepEqual(names, []string{"b", "c", "a"}) {
		t.Fatalf("tool insertion/removal order changed: %v", names)
	}
	collapsed := CollapseSystemMessages(TranscriptContext{Messages: messages})
	if len(collapsed.Messages) != 2 || collapsed.Messages[1].(UserMessage).Content != "first" || GetCurrentSystemPrompt(collapsed.Messages) != GetCurrentSystemPrompt(messages) {
		t.Fatalf("collapse changed replayed state: %+v", collapsed)
	}
	native := ResolveTranscript(TranscriptContext{Messages: messages}, true)
	if len(native.Messages) != len(messages) {
		t.Fatal("native transcript lost message positions")
	}
	transformed := TransformMessages(messages, Model{}, nil)
	if len(transformed) != len(messages) {
		t.Fatalf("message transformations dropped system history: %+v", transformed)
	}
}

func TestTranscriptToolStateChangesAndOutputSchema(t *testing.T) {
	validator := ToolArgumentsValidatorFunc(func(args map[string]any) (map[string]any, error) { return args, nil })
	old := Tool{Name: "lookup", Parameters: map[string]any{"type": "object"}, OutputSchema: map[string]any{"type": "string"}, Validator: validator}
	next := old
	next.OutputSchema = map[string]any{"type": "integer"}
	if DeclarationsEqual(old, next) {
		t.Fatal("output schema change was ignored")
	}
	changes := GetToolStateChanges([]Tool{old, {Name: "removed"}}, []Tool{next, {Name: "added"}})
	if len(changes.ToolsAdded) != 2 || len(changes.ToolsRemoved) != 2 || changes.ToolsRemoved[0].Name != "lookup" || changes.ToolsAdded[0].Validator != nil {
		t.Fatalf("changed tool did not remove then add its declaration: %+v", changes)
	}
	if !DeclarationsEqual(old, ToToolDeclaration(old)) {
		t.Fatal("executable validator affected declaration equality")
	}
}

func TestTranscriptJSONRoundTrip(t *testing.T) {
	ctx := Context{SystemPrompt: "shorthand", Tools: []Tool{{Name: "initial", OutputSchema: map[string]any{"type": "string"}}}, Messages: []Message{
		SystemMessage{Content: "base", Sections: map[string]*string{"value": transcriptString("body"), "remove": nil}, ToolsAdded: []Tool{{Name: "new", Parameters: map[string]any{"type": "object"}, OutputSchema: map[string]any{"type": "integer"}}}, ToolsRemoved: []ToolReference{{Name: "initial"}}, Timestamp: time.Unix(55, 0).UTC()},
		AssistantMessage{ThinkingLevel: ModelThinkingLevelHigh},
	}}
	encoded, err := SerializeContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"role":"system"`) || !strings.Contains(string(encoded), `"remove":null`) || !strings.Contains(string(encoded), `"outputSchema"`) {
		t.Fatalf("missing system declaration fields: %s", encoded)
	}
	restored, err := DeserializeContext(encoded)
	if err != nil {
		t.Fatal(err)
	}
	reencoded, err := SerializeContext(restored)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != string(reencoded) {
		t.Fatalf("context roundtrip mismatch:\n got %+v\nwant %+v", restored, ctx)
	}
	transcript := NormalizeContext(ctx)
	encoded, err = json.Marshal(transcript)
	if err != nil {
		t.Fatal(err)
	}
	var restoredTranscript TranscriptContext
	if err := json.Unmarshal(encoded, &restoredTranscript); err != nil {
		t.Fatal(err)
	}
	reencoded, err = json.Marshal(restoredTranscript)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != string(reencoded) {
		t.Fatalf("transcript roundtrip mismatch:\n got %+v\nwant %+v", restoredTranscript, transcript)
	}
}

func TestTranscriptOpenAIRequestNativeAndCollapsedSystems(t *testing.T) {
	ctx := Context{SystemPrompt: "base", Tools: []Tool{{Name: "old"}}, Messages: []Message{
		UserMessage{Content: "first"},
		SystemMessage{Content: "extra", Sections: map[string]*string{"policy": transcriptString("new policy")}, ToolsRemoved: []ToolReference{{Name: "old"}}, ToolsAdded: []Tool{{Name: "new"}}},
		UserMessage{Content: "last"},
	}}
	for _, native := range []bool{false, true} {
		for _, api := range []API{"openai-responses", "openai-codex-responses", "openai-completions"} {
			t.Run(string(api)+"/native="+map[bool]string{false: "false", true: "true"}[native], func(t *testing.T) {
				model := Model{ID: "fixture", API: api, Provider: "openai", Reasoning: true, MaxTokens: 1024}
				model.Compat = &OpenAIResponsesCompat{SupportsMidConvoSystemMessages: &native}
				if api == "openai-completions" {
					model.Compat = &OpenAICompletionsCompat{SupportsMidConvoSystemMessages: &native}
				}
				var roles, systemText []string
				var toolName, instructions string
				if api == "openai-completions" {
					request := buildOpenAICompletionsRequest(model, ctx, ProviderStreamOptions{})
					for _, message := range request.Messages {
						roles = append(roles, message.Role)
						if message.Role == "developer" {
							systemText = append(systemText, message.Content.(string))
						}
					}
					toolName = request.Tools[0].Function.Name
				} else {
					request := mustBuildOpenAIResponsesRequest(t, nil, model, ctx, ProviderStreamOptions{})
					if api == "openai-codex-responses" {
						request = buildOpenAICodexRequest(nil, model, ctx, ProviderStreamOptions{})
						instructions = request.Instructions
					}
					for _, message := range request.Input {
						role, _ := message["role"].(string)
						roles = append(roles, role)
						if role == "developer" {
							systemText = append(systemText, message["content"].(string))
						}
					}
					toolName = request.Tools[0]["name"].(string)
				}
				wantRoles := []string{"developer", "user", "user"}
				wantSystem := []string{"base\n\nextra\n\nnew policy"}
				if native {
					wantRoles = []string{"developer", "user", "developer", "user"}
					wantSystem = []string{"base", "extra\n\nUpdated system prompt section \"policy\":\n\nnew policy"}
				}
				if api == "openai-codex-responses" {
					wantInstructions := wantSystem[0]
					wantRoles = wantRoles[1:]
					wantSystem = wantSystem[1:]
					if instructions != wantInstructions {
						t.Errorf("instructions=%q want=%q", instructions, wantInstructions)
					}
				}
				if !slices.Equal(roles, wantRoles) || !slices.Equal(systemText, wantSystem) || toolName != "new" {
					t.Fatalf("request transcript mismatch: roles=%v system=%v tool=%q", roles, systemText, toolName)
				}
			})
		}
	}
}

func TestTranscriptUnsupportedProviderRequestsCollapseState(t *testing.T) {
	ctx := Context{SystemPrompt: "base", Tools: []Tool{{Name: "retired"}}, Messages: []Message{
		UserMessage{Content: "first"},
		SystemMessage{Content: "extra", Sections: map[string]*string{"policy": transcriptString("new policy")}, ToolsRemoved: []ToolReference{{Name: "retired"}}, ToolsAdded: []Tool{{Name: "current"}}},
		UserMessage{Content: "last"},
	}}
	for _, api := range []API{"anthropic-messages", "google-generative-ai", "deepseek-chat-completions", "mistral-conversations", "commandcode-custom"} {
		t.Run(string(api), func(t *testing.T) {
			model := Model{ID: "fixture", API: api, MaxTokens: 1024}
			var request any
			var err error
			switch api {
			case "anthropic-messages":
				request = buildAnthropicRequest(model, ctx, AnthropicMessagesProviderOptions{}, false, true)
			case "google-generative-ai":
				request, err = buildGoogleRequest(model, ctx, ProviderStreamOptions{})
			case "deepseek-chat-completions":
				request = buildDeepSeekChatRequest(model, ctx, ProviderStreamOptions{})
			case "mistral-conversations":
				request = buildMistralChatRequest(model, ctx, ProviderStreamOptions{})
			case "commandcode-custom":
				request, _, err = buildCommandCodeRequest(model, ctx, ProviderStreamOptions{})
			}
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			body := string(encoded)
			prompt, _ := json.Marshal("base\n\nextra\n\nnew policy")
			if !strings.Contains(body, string(prompt)) || !strings.Contains(body, `"name":"current"`) || strings.Contains(body, `"name":"retired"`) || !strings.Contains(body, `"first"`) || !strings.Contains(body, `"last"`) {
				t.Fatalf("provider did not replay current prompt/tools: %s", body)
			}
		})
	}
}

func TestTranscriptPublicStreamNormalizesAuthoringContext(t *testing.T) {
	isolateAPIRegistry(t)
	var received []Context
	capture := func(model Model, ctx Context) *AssistantMessageEventStream {
		received = append(received, ctx)
		stream := newAssistantMessageEventStream()
		result := AssistantMessage{API: model.API, StopReason: StopReasonStop}
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventStart, Partial: result})
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventDone, Message: result, Reason: result.StopReason})
		stream.finish(result)
		return stream
	}
	RegisterAPIModule(APIModule{
		API: "transcript-fixture",
		Stream: func(model Model, ctx Context, _ ProviderStreamOptions) *AssistantMessageEventStream {
			return capture(model, ctx)
		},
		StreamSimple: func(model Model, ctx Context, _ SimpleStreamOptions) *AssistantMessageEventStream {
			return capture(model, ctx)
		},
	})
	ctx := Context{SystemPrompt: "base", Tools: []Tool{{Name: "initial"}}, Messages: []Message{UserMessage{Content: "first"}, SystemMessage{Content: "later"}}}
	model := Model{API: "transcript-fixture"}
	Complete(model, ctx, ProviderStreamOptions{})
	CompleteSimple(model, ctx, SimpleStreamOptions{})
	if len(received) != 2 {
		t.Fatalf("missing adapter calls: %d", len(received))
	}
	for _, normalized := range received {
		if normalized.SystemPrompt != "" || len(normalized.Tools) != 0 || len(normalized.Messages) != 3 || normalized.Messages[0].(SystemMessage).Content != "base" || len(GetCurrentTools(normalized.Messages)) != 1 {
			t.Fatalf("adapter received unnormalized context: %+v", normalized)
		}
	}
	if len(ctx.Messages) != 2 || ctx.SystemPrompt != "base" || len(ctx.Tools) != 1 {
		t.Fatal("public streaming modified authoring context")
	}
}
