package pigo

import (
	"reflect"
	"testing"
)

func TestKiroTextProjectionPreservesChronology(t *testing.T) {
	ctx := Context{Messages: []Message{
		UserMessage{Content: "first 中"},
		AssistantMessage{Content: []ContentBlock{TextContent{Text: "answer"}}, StopReason: StopReasonStop},
		&UserMessage{Content: []ContentBlock{&TextContent{Text: "last"}, TextContent{Text: "!"}}},
	}}
	p, err := projectKiroTextConversation(ctx, "fixture-model", false, 3, 100)
	if err != nil || len(p.History) != 2 || p.History[0].User.Content != "first 中" || p.History[1].Assistant.Content != "answer" || p.Current.Content != "last!" || p.SystemPromptLowered {
		t.Fatalf("projection: %#v, %v", p, err)
	}
	if ctx.Messages[0].(UserMessage).Content != "first 中" {
		t.Fatal("caller transcript mutated")
	}
}

func TestKiroTextProjectionSystemRequiresOptIn(t *testing.T) {
	ctx := Context{SystemPrompt: "system", Messages: []Message{UserMessage{Content: "user"}}}
	if _, err := projectKiroTextConversation(ctx, "fixture-model", false, 1, 100); err == nil {
		t.Fatal("implicit system downgrade")
	}
	p, err := projectKiroTextConversation(ctx, "fixture-model", true, 1, 12)
	if err != nil || p.Current.Content != "system\n\nuser" || !p.SystemPromptLowered || ctx.Messages[0].(UserMessage).Content != "user" {
		t.Fatalf("explicit system lowering: %#v, %v", p, err)
	}
	if _, err := projectKiroTextConversation(ctx, "fixture-model", true, 1, 11); err == nil {
		t.Fatal("system separator/text budget ignored")
	}
}

func TestKiroTextProjectionRejectsUnsupportedWithoutRepair(t *testing.T) {
	u := UserMessage{Content: "user"}
	a := AssistantMessage{Content: []ContentBlock{TextContent{Text: "answer"}}, StopReason: StopReasonStop}
	var nilUser *UserMessage
	cases := []Context{
		{}, {Messages: []Message{u, u}}, {Messages: []Message{a}},
		{Messages: []Message{u, u, u}}, {Messages: []Message{nilUser}},
		{Messages: []Message{UserMessage{Content: ""}}},
		{Messages: []Message{UserMessage{Content: " \n\t"}}},
		{Messages: []Message{u, AssistantMessage{Content: a.Content}, u}},
		{Messages: []Message{UserMessage{Content: []ContentBlock{ImageContent{Data: "fixture"}}}}},
		{Messages: []Message{u, AssistantMessage{Content: []ContentBlock{ThinkingContent{Thinking: "fixture"}}, StopReason: StopReasonStop}, u}},
		{Messages: []Message{u, AssistantMessage{Content: []ContentBlock{ToolCall{ID: "fixture", Name: "lookup"}}, StopReason: StopReasonStop}, u}},
		{Messages: []Message{u, ToolResultMessage{ToolCallID: "fixture"}, u}},
		{Messages: []Message{u, SystemMessage{Content: "dynamic"}, u}},
		{Messages: []Message{u}, Tools: []Tool{{Name: "lookup"}}},
		{Messages: []Message{u}, HostedTools: []HostedTool{{Name: "hosted"}}},
		{Messages: []Message{u, AssistantMessage{Content: a.Content, StopReason: StopReasonError}, u}},
		{Messages: []Message{u, AssistantMessage{Content: []ContentBlock{TextContent{Text: "signed", TextSignature: "fixture"}}, StopReason: StopReasonStop}, u}},
		{Messages: []Message{u, AssistantMessage{Content: []ContentBlock{TextContent{Text: " \n\t"}}, StopReason: StopReasonStop}, u}},
		{Messages: []Message{UserMessage{Content: "\xff"}}},
	}
	for index, ctx := range cases {
		p, err := projectKiroTextConversation(ctx, "fixture-model", true, 10, 1024)
		if err == nil || !reflect.DeepEqual(p, kiroTextProjection{}) {
			t.Errorf("case %d repaired or partially returned: %#v, %v", index, p, err)
		}
	}
}

func TestKiroTextProjectionBudgets(t *testing.T) {
	ctx := Context{Messages: []Message{UserMessage{Content: "abcd"}}}
	for _, limits := range [][2]int{{0, 4}, {1, 0}, {1, 3}} {
		if _, err := projectKiroTextConversation(ctx, "fixture-model", false, limits[0], limits[1]); err == nil {
			t.Fatalf("invalid budget accepted: %v", limits)
		}
	}
	if _, err := projectKiroTextConversation(ctx, "", false, 1, 4); err == nil {
		t.Fatal("empty model accepted")
	}
}

func TestKiroTextProjectionSystemOnceAndPointerAssistant(t *testing.T) {
	ctx := Context{SystemPrompt: "s", Messages: []Message{
		UserMessage{Content: "u"},
		&AssistantMessage{Content: []ContentBlock{TextContent{Text: "a"}}, StopReason: StopReasonStop},
		UserMessage{Content: "next"},
	}}
	p, err := projectKiroTextConversation(ctx, "fixture-model", true, 3, 9)
	if err != nil || p.History[0].User.Content != "s\n\nu" || p.Current.Content != "next" {
		t.Fatalf("system not lowered exactly once: %#v, %v", p, err)
	}
	var nilText *TextContent
	ctx = Context{Messages: []Message{UserMessage{Content: []ContentBlock{nilText}}}}
	if _, err := projectKiroTextConversation(ctx, "fixture-model", false, 1, 9); err == nil {
		t.Fatal("nil text block accepted")
	}
}

func TestKiroTextProjectionBoundsEmptyBlocks(t *testing.T) {
	blocks := make([]ContentBlock, maxKiroTextProjectionBlocks+1)
	for i := range blocks {
		blocks[i] = TextContent{}
	}
	blocks[len(blocks)-1] = TextContent{Text: "x"}
	ctx := Context{Messages: []Message{UserMessage{Content: blocks}}}
	if _, err := projectKiroTextConversation(ctx, "fixture-model", false, 1, 1); err == nil {
		t.Fatal("empty blocks bypassed block-count budget")
	}
	blocks[0] = TextContent{Text: "x"}
	ctx.Messages = []Message{
		UserMessage{Content: blocks[:2048]},
		AssistantMessage{Content: blocks[:2048], StopReason: StopReasonStop},
		UserMessage{Content: []ContentBlock{TextContent{Text: "x"}}},
	}
	if _, err := projectKiroTextConversation(ctx, "fixture-model", false, 3, 3); err == nil {
		t.Fatal("block-count budget was not aggregate across messages")
	}
	ctx.Messages = []Message{UserMessage{Content: "  keep surrounding whitespace  "}}
	p, err := projectKiroTextConversation(ctx, "fixture-model", false, 1, 100)
	if err != nil || p.Current.Content != "  keep surrounding whitespace  " {
		t.Fatal("nonempty content was trimmed or rejected")
	}
}
