package pigo

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func kiroRequestFixture() (kiroRequestIdentity, kiroRequestLimits) {
	return kiroRequestIdentity{ConversationID:"fixture-conversation", ProfileARN:"fixture-profile-not-a-credential", Origin:"PI_GO_FIXTURE", ModelID:"fixture-model"}, kiroRequestLimits{Messages:10, Tools:10, PayloadBytes:4096, WireBytes:8192}
}

func TestKiroRequestEnvelopeTextShape(t *testing.T) {
	id, limits := kiroRequestFixture()
	ctx := Context{Messages:[]Message{UserMessage{Content:"first"}, AssistantMessage{StopReason:StopReasonStop, Content:[]ContentBlock{TextContent{Text:"answer"}}}, UserMessage{Content:"next"}}}
	body, err := encodeKiroOfflineRequest(ctx, nil, id, limits)
	if err != nil { t.Fatal(err) }
	// Fixed wire expectation independent of the encoder's private Go structs.
	expected := `{"conversationState":{"chatTriggerType":"MANUAL","agentTaskType":"vibe","conversationId":"fixture-conversation","currentMessage":{"userInputMessage":{"content":"next","modelId":"fixture-model","origin":"PI_GO_FIXTURE"}},"history":[{"userInputMessage":{"content":"first","modelId":"fixture-model","origin":"PI_GO_FIXTURE"}},{"assistantResponseMessage":{"content":"answer"}}]},"profileArn":"fixture-profile-not-a-credential","agentMode":"vibe"}`
	var actualValue, expectedValue any
	if json.Unmarshal(body, &actualValue) != nil || json.Unmarshal([]byte(expected), &expectedValue) != nil || !reflect.DeepEqual(actualValue, expectedValue) { t.Fatalf("unexpected envelope: %s", body) }
	if bytes.Contains(body, []byte("KIRO_CLI")) { t.Fatal("implicit client impersonation") }
	ctx.Messages = []Message{UserMessage{Content:"first"}}
	body, err = encodeKiroOfflineRequest(ctx, nil, id, limits)
	if err != nil || bytes.Contains(body, []byte(`"history"`)) { t.Fatalf("empty history: %s %v", body, err) }
}

func TestKiroRequestEnvelopeToolsPreserveRawAndOwnership(t *testing.T) {
	id, limits := kiroRequestFixture()
	ctx, schemas := kiroToolProjectionFixture()
	body, err := encodeKiroOfflineRequest(ctx, schemas, id, limits)
	if err != nil { t.Fatal(err) }
	var decoded kiroRequestEnvelope
	if err := json.Unmarshal(body, &decoded); err != nil { t.Fatal(err) }
	state := decoded.ConversationState
	if len(state.History)!=2 || state.History[0].User==nil || state.History[1].Assistant==nil || len(state.History[1].Assistant.Calls)!=1 { t.Fatalf("history shape: %s",body) }
	call := state.History[1].Assistant.Calls[0]
	current := state.CurrentMessage.User
	if call.ID!="fixture-1" || call.Name!="lookup" || string(call.Input)!=`{"id":9007199254740993}` || state.History[0].User.Origin!="PI_GO_FIXTURE" || current.Origin!="PI_GO_FIXTURE" || current.Content!="" || current.Context==nil || len(current.Context.Results)!=1 || len(current.Context.Tools)!=1 || current.Context.Results[0].ID!="fixture-1" || current.Context.Results[0].Status!="success" { t.Fatalf("tool payload changed: %s",body) }
	original := append([]byte(nil),body...)
	schemas["lookup"][0]='!'
	if !bytes.Equal(body,original) {t.Fatal("output aliases schema")}
	body[0]='!'
	callIn := ctx.Messages[1].(AssistantMessage).Content[1].(ToolCall)
	if callIn.RawArguments!=`{"id":9007199254740993}` {t.Fatal("output mutation changed input")}
}

func TestKiroRequestEnvelopeInvalidInputsReturnNil(t *testing.T) {
	for _, mutate := range []func(*kiroRequestIdentity,*kiroRequestLimits){
		func(i *kiroRequestIdentity,l *kiroRequestLimits){i.Origin=""},
		func(i *kiroRequestIdentity,l *kiroRequestLimits){i.Origin=" bad"},
		func(i *kiroRequestIdentity,l *kiroRequestLimits){i.Origin="a\nb"},
		func(i *kiroRequestIdentity,l *kiroRequestLimits){i.Origin=string([]byte{255})},
		func(i *kiroRequestIdentity,l *kiroRequestLimits){i.Origin=strings.Repeat("x",65)},
		func(i *kiroRequestIdentity,l *kiroRequestLimits){i.ProfileARN=""},
		func(i *kiroRequestIdentity,l *kiroRequestLimits){i.ConversationID=""},
		func(i *kiroRequestIdentity,l *kiroRequestLimits){i.ModelID=" "},
		func(i *kiroRequestIdentity,l *kiroRequestLimits){l.Messages=0},
		func(i *kiroRequestIdentity,l *kiroRequestLimits){l.Tools=0},
		func(i *kiroRequestIdentity,l *kiroRequestLimits){l.PayloadBytes=1<<20+1},
		func(i *kiroRequestIdentity,l *kiroRequestLimits){l.WireBytes=1<<20+1},
	} {
		id, limits := kiroRequestFixture(); mutate(&id,&limits)
		body,err := encodeKiroOfflineRequest(Context{Messages:[]Message{UserMessage{Content:"hello"}}},nil,id,limits)
		if err==nil || body!=nil {t.Fatalf("invalid input returned bytes: %s %v",body,err)}
	}
	id,limits:=kiroRequestFixture()
	for _,ctx:=range []Context{
		{SystemPrompt:"must not lower",Messages:[]Message{UserMessage{Content:"hello"}}},
		{Messages:[]Message{UserMessage{Content:"hello"},AssistantMessage{Content:[]ContentBlock{TextContent{Text:"unfinished"}}}}},
	} {
		if body,err:=encodeKiroOfflineRequest(ctx,nil,id,limits);err==nil || body!=nil {t.Fatalf("unsupported transcript: %s %v",body,err)}
	}
	ctx:=Context{Messages:[]Message{UserMessage{Content:"hello"}}}
	if body,err:=encodeKiroOfflineRequest(ctx,map[string]json.RawMessage{"unused":json.RawMessage(`{}`)},id,limits);err==nil || body!=nil {t.Fatal("unused schema accepted")}
}

func TestKiroRequestEnvelopeSerializedBudget(t *testing.T) {
	id,limits:=kiroRequestFixture()
	ctx:=Context{Messages:[]Message{UserMessage{Content:strings.Repeat("<",300)}}}
	body,err:=encodeKiroOfflineRequest(ctx,nil,id,limits)
	if err!=nil {t.Fatal(err)}
	limits.WireBytes=len(body)
	if exact,err:=encodeKiroOfflineRequest(ctx,nil,id,limits);err!=nil || !bytes.Equal(exact,body) {t.Fatalf("exact limit: %v",err)}
	limits.WireBytes--
	if rejected,err:=encodeKiroOfflineRequest(ctx,nil,id,limits);err==nil || rejected!=nil {t.Fatal("escaped serialized size not bounded")}
	// A decoded content budget does not stand in for the serialized budget.
	limits.WireBytes=1000
	if rejected,err:=encodeKiroOfflineRequest(ctx,nil,id,limits);err==nil || rejected!=nil {t.Fatal("escape expansion ignored")}
}
