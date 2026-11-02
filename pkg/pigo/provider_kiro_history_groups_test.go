package pigo

import (
	"reflect"
	"testing"
)

func kiroHistoryFixture() Context {
	return Context{Tools: []Tool{{Name: "lookup"}}, Messages: []Message{
		UserMessage{Content: "fixture"},
		AssistantMessage{StopReason: StopReasonToolUse, Content: []ContentBlock{ToolCall{ID: "fixture-1", Name: "lookup"}, &ToolCall{ID: "fixture-2", Name: "lookup"}}},
		ToolResultMessage{ToolCallID: "fixture-1", ToolName: "lookup"},
		&ToolResultMessage{ToolCallID: "fixture-2", ToolName: "lookup", IsError: true},
	}}
}

func TestKiroHistoryGroupsPreserveOrder(t *testing.T) {
	ctx := kiroHistoryFixture()
	groups, err := inspectKiroToolHistoryGroups(ctx, 10, 2)
	want := []kiroToolHistoryGroup{{AssistantIndex: 1, ResultIndices: []int{2, 3}}}
	if err != nil || !reflect.DeepEqual(groups, want) || ctx.Messages[2].(ToolResultMessage).ToolCallID != "fixture-1" {
		t.Fatalf("group projection: %#v, %v", groups, err)
	}
}

func TestKiroHistoryGroupsRejectWithoutRepair(t *testing.T) {
	for _, mutate := range []func(*Context){
		func(c *Context) { c.Messages[2], c.Messages[3] = c.Messages[3], c.Messages[2] },
		func(c *Context) { c.Messages[2] = UserMessage{Content: "interrupt"} },
		func(c *Context) { c.Messages = c.Messages[:3] },
		func(c *Context) { c.Messages = c.Messages[2:] },
		func(c *Context) { c.Tools = nil },
		func(c *Context) { c.Tools = append(c.Tools, Tool{Name: "lookup"}) },
		func(c *Context) { c.Messages[2] = ToolResultMessage{ToolCallID: "fixture-1", ToolName: "other"} },
		func(c *Context) { a := c.Messages[1].(AssistantMessage); a.StopReason = ""; c.Messages[1] = a },
		func(c *Context) {
			a := c.Messages[1].(AssistantMessage)
			a.Content[1] = ToolCall{ID: "fixture-1", Name: "lookup"}
			c.Messages[1] = a
		},
		func(c *Context) {
			a := c.Messages[1].(AssistantMessage)
			a.Content[0] = ToolCall{ID: "provider|foreign", Name: "lookup"}
			c.Messages[1] = a
		},
		func(c *Context) { c.Messages[0] = SystemMessage{Content: "dynamic"} },
		func(c *Context) {
			a := c.Messages[1].(AssistantMessage)
			a.Content[0] = ToolCall{ID: "fixture-1", Name: "lookup", Namespace: "foreign"}
			c.Messages[1] = a
		},
		func(c *Context) {
			a := c.Messages[1].(AssistantMessage)
			a.Content[0] = ToolCall{ID: "fixture-1", Name: "lookup", ThoughtSignature: "fixture"}
			c.Messages[1] = a
		},
		func(c *Context) {
			var call *ToolCall
			a := c.Messages[1].(AssistantMessage)
			a.Content[0] = call
			c.Messages[1] = a
		},
		func(c *Context) { var result *ToolResultMessage; c.Messages[2] = result },
		func(c *Context) { c.Messages = append(c.Messages, c.Messages[1:]...) },
	} {
		ctx := kiroHistoryFixture()
		mutate(&ctx)
		groups, err := inspectKiroToolHistoryGroups(ctx, 10, 10)
		if err == nil || groups != nil {
			t.Fatalf("unsupported history repaired or returned: %#v, %v", groups, err)
		}
	}
}

func TestKiroHistoryGroupsAggregateBlockBudget(t *testing.T) {
	ctx := Context{Messages: []Message{
		AssistantMessage{Content: make([]ContentBlock, 2048), StopReason: StopReasonStop},
		AssistantMessage{Content: make([]ContentBlock, 2049), StopReason: StopReasonStop},
	}}
	if _, err := inspectKiroToolHistoryGroups(ctx, 2, 1); err == nil {
		t.Fatal("aggregate assistant block budget ignored")
	}
}

func TestKiroHistoryGroupsBudgetsAndNativeIDs(t *testing.T) {
	for _, limits := range [][2]int{{3, 2}, {4, 1}, {0, 2}, {4, 0}} {
		if _, err := inspectKiroToolHistoryGroups(kiroHistoryFixture(), limits[0], limits[1]); err == nil {
			t.Fatalf("budget ignored: %v", limits)
		}
	}
	for _, id := range []string{"", "a|b", "中", "a b"} {
		if kiroNativeHistoryToolID(id) {
			t.Errorf("non-native ID accepted: %q", id)
		}
	}
	if !kiroNativeHistoryToolID("aA0_.:-") {
		t.Fatal("valid native ID rejected")
	}
}
