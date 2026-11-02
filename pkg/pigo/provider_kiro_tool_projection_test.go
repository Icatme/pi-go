package pigo

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
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
		func(c *Context, s map[string]json.RawMessage) { c.Messages[0] = UserMessage{Content: []ContentBlock{ImageContent{Data: "unsupported image", MIMEType: "image/png"}}} },
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

func TestKiroToolProjectionRejectsMissingRawAndMismatchedSchemaKey(t *testing.T) {
	ctx, schemas := kiroToolProjectionFixture()
	assistant := ctx.Messages[1].(AssistantMessage)
	call := assistant.Content[1].(ToolCall)
	// Persisted ToolCall history omits RawArguments (json:"-"). A parsed map
	// cannot establish the original wire spelling or its numeric precision.
	call.RawArguments = ""
	call.Arguments = map[string]any{"id": float64(9007199254740992)}
	assistant.Content[1] = call
	ctx.Messages[1] = assistant
	if p, err := projectKiroToolConversation(ctx, "fixture-model", schemas, 10, 10, 4096); err == nil || !reflect.DeepEqual(p, kiroToolProjection{}) {
		t.Fatalf("missing raw evidence accepted: %#v, %v", p, err)
	}

	ctx, schemas = kiroToolProjectionFixture()
	schemas["wrong-name"] = schemas["lookup"]
	delete(schemas, "lookup")
	// Matching map/catalog sizes must not stand in for matching tool names.
	if len(schemas) != len(ctx.Tools) {
		t.Fatal("fixture must preserve schema/catalog count equality")
	}
	if p, err := projectKiroToolConversation(ctx, "fixture-model", schemas, 10, 10, 4096); err == nil || !reflect.DeepEqual(p, kiroToolProjection{}) {
		t.Fatalf("wrong schema key accepted: %#v, %v", p, err)
	}
}

func TestKiroToolProjectionMultipleGroupsAndTools(t *testing.T) {
	ctx, schemas := kiroToolProjectionFixture()
	ctx.Tools = append(ctx.Tools, Tool{Name: "summarize", Description: "second fixture tool"})
	schemas["summarize"] = json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)
	ctx.Messages = append(ctx.Messages,
		&AssistantMessage{StopReason: StopReasonToolUse, Content: []ContentBlock{
			TextContent{Text: "checking both"},
			ToolCall{ID: "fixture-2", Name: "lookup", RawArguments: `{"id":9007199254740995}`},
			&ToolCall{ID: "fixture-3", Name: "summarize", RawArguments: `{"text":"fixture"}`},
		}},
		&ToolResultMessage{ToolCallID: "fixture-2", ToolName: "lookup", Content: []ContentBlock{TextContent{Text: "second result"}}},
		ToolResultMessage{ToolCallID: "fixture-3", ToolName: "summarize", IsError: true, Content: []ContentBlock{TextContent{Text: "third result"}}},
	)
	p, err := projectKiroToolConversation(ctx, "fixture-model", schemas, 6, 3, 4096)
	if err != nil || len(p.History) != 4 || p.Current.Context == nil {
		t.Fatalf("multi-group projection: %#v, %v", p, err)
	}
	for index, entry := range p.History {
		if (index%2 == 0 && entry.User == nil) || (index%2 == 1 && entry.Assistant == nil) {
			t.Fatalf("history does not alternate at %d: %#v", index, entry)
		}
	}
	firstCarrier := p.History[2].User
	if firstCarrier.Content != "" || firstCarrier.Context == nil || len(firstCarrier.Context.Results) != 1 || firstCarrier.Context.Results[0].ID != "fixture-1" || len(firstCarrier.Context.Tools) != 0 {
		t.Fatalf("first group/carrier changed: %#v", firstCarrier)
	}
	secondAssistant := p.History[3].Assistant
	if secondAssistant.Content != "checking both" || len(secondAssistant.Calls) != 2 || secondAssistant.Calls[0].ID != "fixture-2" || secondAssistant.Calls[1].ID != "fixture-3" || string(secondAssistant.Calls[0].Input) != `{"id":9007199254740995}` {
		t.Fatalf("second group call order/precision changed: %#v", secondAssistant)
	}
	results := p.Current.Context.Results
	if p.Current.Content != "" || len(results) != 2 || results[0].ID != "fixture-2" || results[1].ID != "fixture-3" || results[0].Status != "success" || results[1].Status != "error" {
		t.Fatalf("final result carrier changed: %#v", p.Current)
	}
	specs := p.Current.Context.Tools
	if len(specs) != 2 || specs[0].Specification.Name != "lookup" || specs[1].Specification.Name != "summarize" {
		t.Fatalf("current tool catalog changed: %#v", specs)
	}
}

// Synthetic receive -> inert host result -> history, never tool execution.
func TestKiroReceivedToolIdentityRoundTrip(t *testing.T) {
 for _, id := range []string{"aA0_.:-", strings.Repeat("x", 64)} {
  a := kiroTestAssembler(t)
  payload, _ := json.Marshal(map[string]any{"name":"lookup", "toolUseId":id, "input":map[string]any{}, "stop":true})
  tool, err := a.accept(payload)
  if err != nil || tool == nil { t.Fatalf("receive: %v", err) }
  ctx, schemas := kiroToolProjectionFixture()
  ctx.Messages[1] = AssistantMessage{StopReason:StopReasonToolUse, Content:[]ContentBlock{ToolCall{ID:tool.ID, Name:tool.Name, RawArguments:string(tool.Arguments)}}}
  ctx.Messages[2] = ToolResultMessage{ToolCallID:tool.ID, ToolName:tool.Name, Content:[]ContentBlock{TextContent{Text:"synthetic result"}}}
  projected, err := projectKiroToolConversation(ctx, "fixture-model", schemas, 10, 10, 4096)
  if err != nil || projected.Current.Context == nil || len(projected.Current.Context.Results) != 1 || projected.Current.Context.Results[0].ID != id || projected.History[1].Assistant.Calls[0].ID != id { t.Fatalf("roundtrip identity: %#v, %v", projected, err) }
 }
}

func BenchmarkKiroToolProjectionOversizeDescription(b *testing.B) {
 ctx,schemas:=kiroToolProjectionFixture()
 ctx.Tools[0].Description=strings.Repeat("x",1<<20)
 b.ReportAllocs()
 b.ResetTimer()
 for i:=0;i<b.N;i++ {
  if _,err:=projectKiroToolConversation(ctx,"fixture-model",schemas,10,10,64);err==nil { b.Fatal("over-budget description accepted") }
 }
}
