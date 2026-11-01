package pigo

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

type kiroProjectionMustNotMarshal struct{}
func (kiroProjectionMustNotMarshal) MarshalJSON() ([]byte, error) { panic("custom schema marshaler must not run") }

func kiroToolProjectionFixture() (Context, map[string]json.RawMessage) {
	return Context{Tools: []Tool{{Name: "lookup", Description: "fixture", Parameters: kiroProjectionMustNotMarshal{}}}, Messages: []Message{
		UserMessage{Content: "find fixture"},
		AssistantMessage{StopReason: StopReasonToolUse, Content: []ContentBlock{TextContent{Text: "checking"}, ToolCall{ID: "fixture-1", Name: "lookup", RawArguments: `{"id":9007199254740993}`}}},
		ToolResultMessage{ToolCallID: "fixture-1", ToolName: "lookup", Content: []ContentBlock{TextContent{Text: "synthetic result"}}},
	}}, map[string]json.RawMessage{"lookup": json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"}}}`)}
}

func TestKiroToolProjectionEmptyResultCarrierAndRawPrecision(t *testing.T) {
	ctx, schemas := kiroToolProjectionFixture()
	p, err := projectKiroToolConversation(ctx, "fixture-model", schemas, 10, 10, 4096)
	if err != nil || len(p.History) != 2 || p.Current.Content != "" || p.Current.Context == nil || len(p.Current.Context.Results) != 1 || p.Current.Context.Results[0].Content[0].Text != "synthetic result" || p.Current.Context.Results[0].Status != "success" {
		t.Fatalf("projection: %#v, %v", p, err)
	}
	encoded, err := json.Marshal(p.History[1])
	if err != nil || !bytes.Contains(encoded, []byte(`9007199254740993`)) || bytes.Contains(encoded, []byte(`9007199254740992`)) {
		t.Fatalf("raw argument precision lost: %s, %v", encoded, err)
	}
	if p.History[0].User.Content != "find fixture" || p.Current.Context.Results[0].ID != "fixture-1" {
		t.Fatal("original text or tool identity changed")
	}
	schemas["lookup"][0] = '!'
	if p.Current.Context.Tools[0].Specification.InputSchema.JSON[0] != '{' {
		t.Fatal("schema aliases caller buffer")
	}
	p.History[1].Assistant.Calls[0].Input[0] = '!'
	call := ctx.Messages[1].(AssistantMessage).Content[1].(ToolCall)
	if call.RawArguments[0] != '{' { t.Fatal("arguments alias caller state") }
}

func TestKiroToolProjectionRejectsUnsupportedInputs(t *testing.T) {
	for _, mutate := range []func(*Context, map[string]json.RawMessage){
		func(c *Context, s map[string]json.RawMessage) { c.SystemPrompt = "system" },
		func(c *Context, s map[string]json.RawMessage) { delete(s, "lookup") },
		func(c *Context, s map[string]json.RawMessage) { s["lookup"] = json.RawMessage(`{"type":"object","type":"array"}`) },
		func(c *Context, s map[string]json.RawMessage) { c.Messages[0] = UserMessage{Content: []ContentBlock{TextContent{Text: "blocks unsupported"}}} },
		func(c *Context, s map[string]json.RawMessage) { a := c.Messages[1].(AssistantMessage); call := a.Content[1].(ToolCall); call.Arguments = map[string]any{"id":1}; a.Content[1] = call; c.Messages[1] = a },
		func(c *Context, s map[string]json.RawMessage) { a := c.Messages[1].(AssistantMessage); call := a.Content[1].(ToolCall); call.RawArguments = `{"x":1,"x":2}`; a.Content[1] = call; c.Messages[1] = a },
		func(c *Context, s map[string]json.RawMessage) { a := c.Messages[1].(AssistantMessage); a.Content[0] = ThinkingContent{Thinking:"fixture"}; c.Messages[1] = a },
		func(c *Context, s map[string]json.RawMessage) { r := c.Messages[2].(ToolResultMessage); r.Content = []ContentBlock{ImageContent{Data:"fixture"}}; c.Messages[2] = r },
		func(c *Context, s map[string]json.RawMessage) { c.Messages = append(c.Messages, UserMessage{Content:"interrupted chronology"}) },
		func(c *Context, s map[string]json.RawMessage) { c.Tools[0].OutputSchema = map[string]any{} },
		func(c *Context, s map[string]json.RawMessage) { a := c.Messages[1].(AssistantMessage); a.Content = append(a.Content, TextContent{Text:"after tool"}); c.Messages[1] = a },
		func(c *Context, s map[string]json.RawMessage) { r := c.Messages[2].(ToolResultMessage); r.Details = map[string]any{"fixture":true}; c.Messages[2] = r },
	} {
		ctx, schemas := kiroToolProjectionFixture()
		mutate(&ctx, schemas)
		p, err := projectKiroToolConversation(ctx, "fixture-model", schemas, 10, 10, 4096)
		if err == nil || !reflect.DeepEqual(p, kiroToolProjection{}) {
			t.Fatalf("unsupported input repaired/partially returned: %#v, %v", p, err)
		}
	}
}

func TestKiroToolProjectionBudgetsAndErrorResult(t *testing.T) {
	ctx, schemas := kiroToolProjectionFixture()
	if _, err := projectKiroToolConversation(ctx, "fixture-model", schemas, 10, 10, 2); err == nil {
		t.Fatal("aggregate content/schema budget ignored")
	}
	r := ctx.Messages[2].(ToolResultMessage)
	r.IsError = true
	ctx.Messages[2] = &r
	p, err := projectKiroToolConversation(ctx, "fixture-model", schemas, 10, 10, 4096)
	if err != nil || p.Current.Context.Results[0].Status != "error" {
		t.Fatalf("error result not preserved: %#v, %v", p, err)
	}
}
