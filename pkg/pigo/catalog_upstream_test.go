package pigo

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestSeptemberUpstreamCatalog(t *testing.T) {
	for _, provider := range []Provider{"openai", "openai-codex"} {
		model := GetModel(provider, "gpt-6-astra")
		if model == nil {
			t.Fatalf("%s Astra missing", provider)
		}
		if model.ContextWindow != 272000 || model.MaxTokens != 128000 || !slices.Contains(model.Input, InputImage) {
			t.Fatalf("unexpected Astra catalog: %+v", model)
		}
		if got := GetSupportedThinkingLevels(*model); !slices.Equal(got, []ModelThinkingLevel{ModelThinkingLevelLow, ModelThinkingLevelMedium, ModelThinkingLevelHigh, ModelThinkingLevelXHigh, ModelThinkingLevelMax}) {
			t.Fatalf("Astra levels: %v", got)
		}
		if got := ClampThinkingLevel(*model, ModelThinkingLevelOff); got != ModelThinkingLevelLow {
			t.Fatalf("Astra off clamp: %s", got)
		}
		if model.Cost.Input != 10 || model.Cost.Output != 50 || len(model.CostTiers) != 1 {
			t.Fatalf("Astra cost snapshot: %+v", model)
		}
	}
	for _, id := range []string{"gpt-5.4", "gpt-5.4-mini"} {
		if GetModel("openai-codex", id) != nil {
			t.Fatalf("retired Codex model remains: %s", id)
		}
		if GetModel("openai", id) == nil {
			t.Fatalf("OpenAI model incorrectly removed: %s", id)
		}
	}
	if GetModel("deepseek", "deepseek-v4-flash") != nil {
		t.Fatal("retired native Flash remains")
	}
	flash := GetModel("deepseek", "deepseek-flash")
	if flash == nil || flash.ContextWindow != 1000000 || flash.MaxTokens != 384000 || !slices.Contains(flash.Input, InputImage) {
		t.Fatalf("unexpected Flash: %+v", flash)
	}
	if GetModel("opencode-go", "deepseek-v4-flash") == nil {
		t.Fatal("OpenCode provider id was incorrectly renamed")
	}
}

func TestDeepSeekFlashImageAndReasoningWire(t *testing.T) {
	model := *GetModel("deepseek", "deepseek-flash")
	request := buildDeepSeekChatRequest(model, Context{Messages: []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "describe"}, ImageContent{Data: "aW1n", MIMEType: "image/png"}}}}}, ProviderStreamOptions{Reasoning: ThinkingLevelLow})
	wire, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(wire, &body); err != nil {
		t.Fatal(err)
	}
	if body["reasoning_effort"] != "low" {
		t.Fatalf("effort must be top-level: %s", wire)
	}
	if _, present := body["thinking"].(map[string]any)["reasoning_effort"]; present {
		t.Fatalf("effort nested inside thinking: %s", wire)
	}
	content := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if got := content[1].(map[string]any)["image_url"].(map[string]any)["url"]; got != "data:image/png;base64,aW1n" {
		t.Fatalf("image lost: %s", wire)
	}
	off := buildDeepSeekChatRequest(model, Context{}, ProviderStreamOptions{Reasoning: ThinkingLevel("off")})
	if off.Thinking.Type != "disabled" || off.ReasoningEffort != "" {
		t.Fatalf("off enables thinking: %+v", off)
	}
}

func TestNewCompatFactsAreIndependentCopies(t *testing.T) {
	model := GetModel("openai", "gpt-6-astra")
	*model.Compat.(*OpenAIResponsesCompat).SupportsExplicitPromptCacheMode = false
	if !*GetModel("openai", "gpt-6-astra").Compat.(*OpenAIResponsesCompat).SupportsExplicitPromptCacheMode {
		t.Fatal("catalog mutated through returned model")
	}
	flag := true
	original := Model{Compat: &AnthropicMessagesCompat{ForceAdaptiveThinking: &flag}}
	copied := cloneModel(original)
	*copied.Compat.(*AnthropicMessagesCompat).ForceAdaptiveThinking = false
	if !flag {
		t.Fatal("adaptive thinking fact aliases input")
	}
}

func TestAnthropicAdaptiveThinkingCatalogOverride(t *testing.T) {
	enabled := true
	model := Model{ID: "claude-fable-5", Reasoning: true, Compat: &AnthropicMessagesCompat{ForceAdaptiveThinking: &enabled}, ThinkingLevelMap: ThinkingLevelMap{ModelThinkingLevelXHigh: "xhigh"}}
	request := buildAnthropicRequest(model, Context{Tools: []Tool{{Name: "lookup"}}}, AnthropicMessagesProviderOptions{StreamOptions: StreamOptions{Reasoning: ThinkingLevelXHigh}}, false, true)
	if request.Thinking.(map[string]any)["type"] != "adaptive" || request.OutputConfig == nil || request.OutputConfig.Effort != "xhigh" {
		t.Fatalf("missing catalog effort: %+v", request)
	}
	encoded, err := json.Marshal(request.Tools)
	if err != nil {
		t.Fatal(err)
	}
	var tools []map[string]any
	if err := json.Unmarshal(encoded, &tools); err != nil {
		t.Fatal(err)
	}
	if _, present := tools[0]["cache_control"]; present {
		t.Fatalf("unexpected tool cache control: %s", encoded)
	}
}

func TestDeepSeekFlashToolImagesAndMinimalReasoning(t *testing.T) {
	model := *GetModel("deepseek", "deepseek-flash")
	messages := []Message{
		AssistantMessage{Provider: model.Provider, API: model.API, Model: model.ID, Content: []ContentBlock{ToolCall{ID: "shot", Name: "screenshot", Arguments: map[string]any{}}}, StopReason: StopReasonToolUse},
		ToolResultMessage{ToolCallID: "shot", ToolName: "screenshot", Content: []ContentBlock{ImageContent{Data: "aW1n", MIMEType: "image/png"}}},
	}
	request := buildDeepSeekChatRequest(model, Context{Messages: messages}, ProviderStreamOptions{Reasoning: ThinkingLevelMinimal})
	if request.ReasoningEffort != "low" {
		t.Fatalf("minimal effort = %q, want low", request.ReasoningEffort)
	}
	wire, err := json.Marshal(request.Messages)
	if err != nil {
		t.Fatal(err)
	}
	var sent []map[string]any
	if err := json.Unmarshal(wire, &sent); err != nil {
		t.Fatal(err)
	}
	parts, ok := sent[1]["content"].([]any)
	if !ok || len(parts) != 1 {
		t.Fatalf("tool image omitted: %s", wire)
	}
	if got := parts[0].(map[string]any)["image_url"].(map[string]any)["url"]; got != "data:image/png;base64,aW1n" {
		t.Fatalf("tool image lost: %s", wire)
	}
}

func TestResponsesCompatSwitchCopiesAreIndependent(t *testing.T) {
	for name, selectFlag := range map[string]func(*OpenAIResponsesCompat) **bool{
		"session header": func(c *OpenAIResponsesCompat) **bool { return &c.SendSessionIdHeader },
		"long cache":     func(c *OpenAIResponsesCompat) **bool { return &c.SupportsLongCacheRetention },
		"explicit cache": func(c *OpenAIResponsesCompat) **bool { return &c.SupportsExplicitPromptCacheMode },
		"output limit":   func(c *OpenAIResponsesCompat) **bool { return &c.SupportsMaxOutputTokens },
	} {
		t.Run(name, func(t *testing.T) {
			original := &OpenAIResponsesCompat{}
			if *selectFlag(cloneCompat(original).(*OpenAIResponsesCompat)) != nil {
				t.Fatal("unset capability became known")
			}
			enabled := true
			*selectFlag(original) = &enabled
			copied := cloneCompat(original).(*OpenAIResponsesCompat)
			**selectFlag(copied) = false
			if !**selectFlag(original) {
				t.Fatal("returned model aliases the original capability")
			}
		})
	}
}

func TestNativeDeepSeekThinkingCapabilitiesAndWire(t *testing.T) {
	for _, test := range []struct {
		id      string
		levels  []ModelThinkingLevel
		minimal string
	}{
		{"deepseek-flash", []ModelThinkingLevel{ModelThinkingLevelOff, ModelThinkingLevelLow, ModelThinkingLevelHigh, ModelThinkingLevelMax}, "low"},
		{"deepseek-v4-pro", []ModelThinkingLevel{ModelThinkingLevelOff, ModelThinkingLevelHigh, ModelThinkingLevelMax}, "high"},
	} {
		t.Run(test.id, func(t *testing.T) {
			model := *GetModel("deepseek", test.id)
			if got := GetSupportedThinkingLevels(model); !slices.Equal(got, test.levels) {
				t.Fatalf("supported levels = %v, want %v", got, test.levels)
			}
			for _, requested := range []ThinkingLevel{ThinkingLevelMinimal, ThinkingLevelLow} {
				request := buildDeepSeekChatRequest(model, Context{}, ProviderStreamOptions{Reasoning: requested})
				if request.ReasoningEffort != test.minimal {
					t.Fatalf("requested %s emits %s, want %s", requested, request.ReasoningEffort, test.minimal)
				}
			}
			request := buildDeepSeekChatRequest(model, Context{}, ProviderStreamOptions{Reasoning: ThinkingLevelXHigh})
			if request.ReasoningEffort != "max" {
				t.Fatalf("xhigh should clamp to max, got %s", request.ReasoningEffort)
			}
		})
	}
}
