package pigo

import (
 "bytes"
 "context"
 "encoding/json"
 "reflect"
 "testing"
)

// Integration-only fixture: the result is fabricated locally, not executed.
// This is explicit raw-evidence transfer, not a production provider adapter.
func TestKiroOfflineIntegrationToolRoundTrip(t *testing.T) {
 for _,isError:=range []bool{false,true} {
  ctx,schemas:=kiroToolProjectionFixture()
  ctx.Messages=[]Message{UserMessage{Content:"find fixture"}}
  initial,err:=projectKiroToolConversation(ctx,"fixture-model",schemas,10,10,4096)
  if err!=nil || len(initial.History)!=0 || initial.Current.Context==nil || len(initial.Current.Context.Tools)!=1 || initial.Current.Context.Tools[0].Specification.Name!="lookup" {
   t.Fatalf("initial DTO: %#v %v",initial,err)
  }
  wire:=kiroOfflineJoin(
   kiroOfflineEvent("toolUseEvent",`{"name":"lookup","toolUseId":"roundtrip-1","input":{"id":9007199254740993},"stop":true}`),
   kiroOfflineEvent("metadataEvent",`{"stopReason":"TOOL_USE","tokenUsage":{"outputTokens":2}}`),
  )
  response,err:=collectKiroOfflineResponse(context.Background(),kiroFragmentReader{bytes.NewReader(wire)},kiroOfflineTestLimits())
  if err!=nil || response.StopReason!=StopReasonToolUse || len(response.Parts)!=1 || response.Parts[0].Tool==nil { t.Fatalf("response: %#v %v",response,err) }
  evidence:=response.Parts[0].Tool
  call:=ToolCall{ID:evidence.ID,Name:evidence.Name,RawArguments:string(evidence.Arguments)}
  ctx.Messages=append(ctx.Messages,
   AssistantMessage{StopReason:response.StopReason,Content:[]ContentBlock{call}},
   ToolResultMessage{ToolCallID:call.ID,ToolName:call.Name,IsError:isError,Content:[]ContentBlock{TextContent{Text:"synthetic result"}}},
  )
  next,err:=projectKiroToolConversation(ctx,"fixture-model",schemas,10,10,4096)
  if err!=nil || len(next.History)!=2 || next.History[1].Assistant==nil || len(next.History[1].Assistant.Calls)!=1 || next.Current.Context==nil || len(next.Current.Context.Results)!=1 { t.Fatalf("next DTO: %#v %v",next,err) }
  wireCall:=next.History[1].Assistant.Calls[0]
  result:=next.Current.Context.Results[0]
  expectedStatus:="success";if isError {expectedStatus="error"}
  if wireCall.ID!=call.ID || result.ID!=call.ID || wireCall.Name!=call.Name || result.Status!=expectedStatus || len(result.Content)!=1 || result.Content[0].Text!="synthetic result" || next.Current.Content!="" || string(wireCall.Input)!=`{"id":9007199254740993}` {
   t.Fatalf("identity/precision/status changed: %#v %#v",wireCall,result)
  }
  encoded,err:=json.Marshal(next.History)
  if err!=nil || !bytes.Contains(encoded,[]byte("9007199254740993")) { t.Fatalf("wire number lost: %s %v",encoded,err) }

  // RawArguments intentionally disappears on normal persistence. Do not
  // invent it by reserializing a float map or silently clear Arguments.
  saved,err:=json.Marshal(call);if err!=nil {t.Fatal(err)}
  var restored ToolCall
  if err:=json.Unmarshal(saved,&restored);err!=nil {t.Fatal(err)}
  if restored.RawArguments!="" {t.Fatal("persistence contract changed; revisit replay policy")}
  ctx.Messages[1]=AssistantMessage{StopReason:StopReasonToolUse,Content:[]ContentBlock{restored}}
  rejected,err:=projectKiroToolConversation(ctx,"fixture-model",schemas,10,10,4096)
  if err==nil || !reflect.DeepEqual(rejected,kiroToolProjection{}) {t.Fatalf("restored history invented raw evidence: %#v %v",rejected,err)}
 }
}

func TestKiroOfflineIntegrationTextRoundTrip(t *testing.T) {
 ctx:=Context{Messages:[]Message{UserMessage{Content:"first"}}}
 initial,err:=projectKiroTextConversation(ctx,"fixture-model",false,10,4096)
 if err!=nil || initial.Current.Content!="first" {t.Fatalf("initial text: %#v %v",initial,err)}
 wire:=kiroOfflineJoin(
  kiroOfflineEvent("assistantResponseEvent",`{"content":"synthetic answer"}`),
  kiroOfflineEvent("metadataEvent",`{"stopReason":"END_TURN"}`),
 )
 response,err:=collectKiroOfflineResponse(context.Background(),bytes.NewReader(wire),kiroOfflineTestLimits())
 if err!=nil || response.StopReason!=StopReasonStop || len(response.Parts)!=1 || response.Parts[0].Kind!="text" {t.Fatalf("text response: %#v %v",response,err)}
 ctx.Messages=append(ctx.Messages,AssistantMessage{StopReason:response.StopReason,Content:[]ContentBlock{TextContent{Text:response.Parts[0].Text}}},UserMessage{Content:"next"})
 next,err:=projectKiroTextConversation(ctx,"fixture-model",false,10,4096)
 if err!=nil || len(next.History)!=2 || next.History[1].Assistant==nil || next.History[1].Assistant.Content!="synthetic answer" || next.Current.Content!="next" {t.Fatalf("text replay: %#v %v",next,err)}
}
