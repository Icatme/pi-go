package pigo

import "testing"

func TestResponsesAdditionalToolsExplicitOptOut(t *testing.T) {
	for _, remove := range []bool{false, true} {
		model := *GetModel("openai", "gpt-6-astra")
		compat := model.Compat.(*OpenAIResponsesCompat)
		disabled := false
		compat.SupportsAdditionalTools = &disabled
		ctx := Context{Messages: []Message{SystemMessage{Content: "base", ToolsAdded: []Tool{anchorFunction("first")}}, UserMessage{Content: "before"}, SystemMessage{Content: "late instructions", ToolsAdded: []Tool{anchorFunction("late")}}, UserMessage{Content: "next"}}}
		if remove {
			ctx.Messages = append(ctx.Messages, SystemMessage{ToolsRemoved: []ToolReference{{Name: "first"}}})
		}
		req := mustBuildOpenAIResponsesRequest(t, nil, model, ctx, ProviderStreamOptions{})
		want := 2
		if remove {
			want = 1
		}
		if len(req.Tools) != want || countToolAnchors(req.Input) != 0 {
			t.Fatalf("lost or duplicated tools: %+v", req)
		}
		if !supportsTranscriptSystemMessages(model) {
			t.Fatal("tool opt-out disabled system message support")
		}
		found := false
		for _, item := range req.Input {
			if item["role"] == "developer" {
				found = true
			}
		}
		if !found {
			t.Fatal("system transcript removed")
		}
		if _, err := buildOpenAIResponsesRequest(nil, model, ctx, ProviderStreamOptions{PreviousResponseID: "previous"}); err == nil {
			t.Fatal("dynamic full-history guard lost")
		}
	}
}
func TestResponsesAdditionalToolsTrueDoesNotGrantCapability(t *testing.T) {
	enabled := true
	model := Model{ID: "unknown", API: "openai-responses", Provider: "fixture", Compat: &OpenAIResponsesCompat{SupportsAdditionalTools: &enabled, SupportsMidConvoSystemMessages: &enabled}}
	ctx := Context{Messages: []Message{UserMessage{Content: "before"}, SystemMessage{ToolsAdded: []Tool{anchorFunction("late")}}}}
	req := mustBuildOpenAIResponsesRequest(t, nil, model, ctx, ProviderStreamOptions{})
	if len(req.Tools) != 1 || countToolAnchors(req.Input) != 0 {
		t.Fatal("unknown model gained native capability")
	}
}

func TestResponsesAdditionalToolsOptOutPreservesReplayAndRedeclaration(t *testing.T) {
	model := *GetModel("openai", "gpt-6-astra")
	disabled := false
	model.Compat.(*OpenAIResponsesCompat).SupportsAdditionalTools = &disabled
	old := anchorFunction("lookup")
	updated := anchorFunction("lookup")
	updated.Description = "new definition"
	ctx := Context{Messages: []Message{SystemMessage{ToolsAdded: []Tool{old}}, UserMessage{Content: "do lookup"}, AssistantMessage{API: model.API, Provider: model.Provider, Model: model.ID, StopReason: StopReasonToolUse, Content: []ContentBlock{ToolCall{ID: "call_lookup|fc_lookup", Name: "lookup", Arguments: map[string]any{}}}}, ToolResultMessage{ToolCallID: "call_lookup|fc_lookup", ToolName: "lookup", Content: []ContentBlock{TextContent{Text: "result"}}}, SystemMessage{ToolsRemoved: []ToolReference{{Name: "lookup"}}, ToolsAdded: []Tool{updated}}, UserMessage{Content: "use updated"}}}
	req := mustBuildOpenAIResponsesRequest(t, nil, model, ctx, ProviderStreamOptions{})
	if len(req.Tools) != 1 || req.Tools[0]["name"] != "lookup" || req.Tools[0]["description"] != "new definition" || countToolAnchors(req.Input) != 0 {
		t.Fatalf("bad current declarations: %+v", req)
	}
	calls, results := 0, 0
	for _, item := range req.Input {
		switch item["type"] {
		case "function_call":
			calls++
		case "function_call_output":
			results++
		}
	}
	if calls != 1 || results != 1 {
		t.Fatalf("history lost or duplicated: %+v", req.Input)
	}
}
