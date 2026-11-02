package pigo

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// Original synthetic cases mapped to upstream history/transform scenarios in
// docs/kiro-history-test-migration.md. No session payload or upstream code copied.
func kiroHistoryMigrationFixture() (Context, map[string]json.RawMessage) {
	return Context{
			Tools: []Tool{{Name: "inventory"}, {Name: "notes"}},
			Messages: []Message{
				UserMessage{Content: "Inspect synthetic items"},
				AssistantMessage{StopReason: StopReasonToolUse, Content: []ContentBlock{
					TextContent{Text: "Checking "}, TextContent{Text: "fixtures."},
					ToolCall{ID: "case:inventory-1", Name: "inventory", RawArguments: ` {"item":9007199254740993,"nested":{"delta":-9007199254740995,"label":"示例"}} `},
					ToolCall{ID: "case:notes-2", Name: "notes", RawArguments: `{}`},
				}},
				ToolResultMessage{ToolCallID: "case:inventory-1", ToolName: "inventory", Content: []ContentBlock{TextContent{Text: "first"}, TextContent{Text: " result"}}},
				ToolResultMessage{ToolCallID: "case:notes-2", ToolName: "notes", IsError: true},
			},
		}, map[string]json.RawMessage{
			"inventory": json.RawMessage(`{"type":"object","properties":{"item":{"type":"integer"}},"required":["item"]}`),
			"notes":     json.RawMessage(`{"type":"object"}`),
		}
}

func kiroHistoryMigrationEncode(ctx Context, schemas map[string]json.RawMessage) ([]byte, error) {
	return encodeKiroOfflineRequest(ctx, schemas,
		kiroRequestIdentity{ConversationID: "synthetic-history", ProfileARN: "synthetic-profile", Origin: "fixture-client", ModelID: "fixture-model"},
		kiroRequestLimits{Messages: 20, Tools: 10, PayloadBytes: 16384, WireBytes: 32768})
}

func TestKiroHistoryMigrationOriginalArgumentsAndEmptyResultCarrier(t *testing.T) {
	ctx, schemas := kiroHistoryMigrationFixture()
	wantCtx, wantSchemas := kiroHistoryMigrationFixture()
	projection, err := projectKiroToolConversation(ctx, "fixture-model", schemas, 20, 10, 16384)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.History) != 2 || projection.History[1].Assistant == nil || len(projection.History[1].Assistant.Calls) != 2 {
		t.Fatalf("history shape: %#v", projection)
	}
	for i, call := range projection.History[1].Assistant.Calls {
		original := ctx.Messages[1].(AssistantMessage).Content[i+2].(ToolCall)
		if call.ID != original.ID || call.Name != original.Name || string(call.Input) != original.RawArguments {
			t.Fatalf("raw evidence changed: %#v", call)
		}
	}
	wire, err := kiroHistoryMigrationEncode(ctx, schemas)
	if err != nil {
		t.Fatal(err)
	}
	var got kiroRequestEnvelope
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	history := got.ConversationState.History
	if len(history) != 2 || history[0].User == nil || history[0].User.Content != "Inspect synthetic items" || history[1].Assistant == nil || history[1].Assistant.Content != "Checking fixtures." {
		t.Fatalf("chronology/text changed: %#v", history)
	}
	if len(history[1].Assistant.Calls) != 2 {
		t.Fatal("calls dropped")
	}
	for i, call := range history[1].Assistant.Calls {
		original := ctx.Messages[1].(AssistantMessage).Content[i+2].(ToolCall)
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(original.RawArguments)); err != nil {
			t.Fatal(err)
		}
		if call.ID != original.ID || call.Name != original.Name || !bytes.Equal(call.Input, compact.Bytes()) {
			t.Fatalf("serialized evidence changed: %#v", call)
		}
	}
	for _, literal := range []string{"9007199254740993", "-9007199254740995"} {
		if !bytes.Contains(history[1].Assistant.Calls[0].Input, []byte(literal)) {
			t.Fatalf("integer precision changed: %s", wire)
		}
	}
	current := got.ConversationState.CurrentMessage.User
	if current.Content != "" || current.Context == nil || len(current.Context.Results) != 2 || len(current.Context.Tools) != 2 {
		t.Fatalf("empty carrier/catalog changed: %#v", current)
	}
	for i, tool := range current.Context.Tools {
		if tool.Specification.Name != ctx.Tools[i].Name || !bytes.Equal(tool.Specification.InputSchema.JSON, schemas[ctx.Tools[i].Name]) {
			t.Fatalf("schema/catalog changed: %#v", tool)
		}
	}
	for i, result := range current.Context.Results {
		original := ctx.Messages[i+2].(ToolResultMessage)
		wantStatus, wantText := "success", "first result"
		if i == 1 {
			wantStatus, wantText = "error", ""
		}
		if result.ID != original.ToolCallID || result.Status != wantStatus || len(result.Content) != 1 || result.Content[0].Text != wantText {
			t.Fatalf("result changed: %#v", result)
		}
	}
	if !reflect.DeepEqual(ctx, wantCtx) || !reflect.DeepEqual(schemas, wantSchemas) {
		t.Fatal("projection mutated original context/schema")
	}
}

func TestKiroHistoryMigrationRejectsRepairInsteadOfInventingHistory(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Context, map[string]json.RawMessage)
	}{
		{"missing_result", func(c *Context, _ map[string]json.RawMessage) { c.Messages = c.Messages[:3] }},
		{"orphan_result", func(c *Context, _ map[string]json.RawMessage) {
			r := c.Messages[2].(ToolResultMessage)
			r.ToolCallID = "missing"
			c.Messages[2] = r
		}},
		{"results_reordered", func(c *Context, _ map[string]json.RawMessage) {
			c.Messages[2], c.Messages[3] = c.Messages[3], c.Messages[2]
		}},
		{"human_interrupts_group", func(c *Context, _ map[string]json.RawMessage) {
			c.Messages = append(c.Messages[:2], append([]Message{UserMessage{Content: "Do not reorder me"}}, c.Messages[2:]...)...)
		}},
		{"leading_orphan", func(c *Context, _ map[string]json.RawMessage) {
			c.Messages = append([]Message{c.Messages[2]}, c.Messages...)
		}},
		{"missing_historical_catalog", func(c *Context, s map[string]json.RawMessage) { c.Tools = c.Tools[:1]; delete(s, "notes") }},
		{"no_catalog", func(c *Context, s map[string]json.RawMessage) { c.Tools = nil; clear(s) }},
		{"duplicate_call_id", func(c *Context, _ map[string]json.RawMessage) {
			a := c.Messages[1].(AssistantMessage)
			call := a.Content[3].(ToolCall)
			call.ID = "case:inventory-1"
			a.Content[3] = call
			c.Messages[1] = a
		}},
		{"non_native_id", func(c *Context, _ map[string]json.RawMessage) {
			a := c.Messages[1].(AssistantMessage)
			call := a.Content[2].(ToolCall)
			call.ID = "foreign/id"
			a.Content[2] = call
			c.Messages[1] = a
			r := c.Messages[2].(ToolResultMessage)
			r.ToolCallID = call.ID
			c.Messages[2] = r
		}},
		{"parsed_and_raw_conflict", func(c *Context, _ map[string]json.RawMessage) {
			a := c.Messages[1].(AssistantMessage)
			call := a.Content[2].(ToolCall)
			call.Arguments = map[string]any{"item": 42}
			a.Content[2] = call
			c.Messages[1] = a
		}},
		{"missing_raw_evidence", func(c *Context, _ map[string]json.RawMessage) {
			a := c.Messages[1].(AssistantMessage)
			call := a.Content[2].(ToolCall)
			call.RawArguments = ""
			a.Content[2] = call
			c.Messages[1] = a
		}},
		{"duplicate_argument_key", func(c *Context, _ map[string]json.RawMessage) {
			a := c.Messages[1].(AssistantMessage)
			call := a.Content[2].(ToolCall)
			call.RawArguments = `{"item":1,"item":2}`
			a.Content[2] = call
			c.Messages[1] = a
		}},
		{"unpaired_surrogate", func(c *Context, _ map[string]json.RawMessage) {
			a := c.Messages[1].(AssistantMessage)
			call := a.Content[2].(ToolCall)
			call.RawArguments = `{"label":"\ud800"}`
			a.Content[2] = call
			c.Messages[1] = a
		}},
		{"incomplete_assistant", func(c *Context, _ map[string]json.RawMessage) {
			a := c.Messages[1].(AssistantMessage)
			a.StopReason = StopReasonLength
			c.Messages[1] = a
		}},
		{"reasoning_not_flattened", func(c *Context, _ map[string]json.RawMessage) {
			a := c.Messages[1].(AssistantMessage)
			a.Content = append([]ContentBlock{ThinkingContent{Thinking: "private synthetic reasoning"}}, a.Content...)
			c.Messages[1] = a
		}},
		{"image_not_silently_stripped", func(c *Context, _ map[string]json.RawMessage) {
			c.Messages[0] = UserMessage{Content: []ContentBlock{TextContent{Text: "image"}, ImageContent{Data: "synthetic"}}}
		}},
		{"system_not_lowered", func(c *Context, _ map[string]json.RawMessage) { c.SystemPrompt = "explicit system instruction" }},
		{"adjacent_humans_not_merged", func(c *Context, _ map[string]json.RawMessage) {
			c.Messages = append([]Message{UserMessage{Content: "earlier human"}}, c.Messages...)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, schemas := kiroHistoryMigrationFixture()
			tc.mutate(&ctx, schemas)
			wantCtx, wantSchemas := kiroHistoryMigrationFixture()
			tc.mutate(&wantCtx, wantSchemas)
			wire, err := kiroHistoryMigrationEncode(ctx, schemas)
			if err == nil || wire != nil {
				t.Fatalf("unsupported history repaired/emitted: %s %v", wire, err)
			}
			if !reflect.DeepEqual(ctx, wantCtx) || !reflect.DeepEqual(schemas, wantSchemas) {
				t.Fatal("failed projection mutated input")
			}
		})
	}
}

func TestKiroHistoryMigrationEmptyResultRepresentations(t *testing.T) {
	for _, blocks := range [][]ContentBlock{nil, {}, {TextContent{Text: ""}}, {&TextContent{Text: ""}}} {
		ctx, schemas := kiroHistoryMigrationFixture()
		r := ctx.Messages[2].(ToolResultMessage)
		r.Content = blocks
		ctx.Messages[2] = r
		wire, err := kiroHistoryMigrationEncode(ctx, schemas)
		if err != nil {
			t.Fatal(err)
		}
		var got kiroRequestEnvelope
		if err := json.Unmarshal(wire, &got); err != nil {
			t.Fatal(err)
		}
		user := got.ConversationState.CurrentMessage.User
		if user.Content != "" || user.Context == nil || len(user.Context.Results) != 2 || len(user.Context.Results[0].Content) != 1 || user.Context.Results[0].Content[0].Text != "" {
			t.Fatalf("invented result/carrier text: %s", wire)
		}
	}
}
