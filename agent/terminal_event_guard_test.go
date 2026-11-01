package agent

import (
 "context"
 "errors"
 "sync/atomic"
 "testing"
)

func TestTerminalErrorEventWithoutCauseCannotReviveToolCalls(t *testing.T) {
 var calls atomic.Int32
 turns:=0
 definition,err:= (AgentDefinition{
  Model:staticModel{streamFn:func(context.Context,ModelRequest)(AssistantStream,error){
   turns++
   if turns>1 { return newStaticAssistantStream(Message{Role:RoleAssistant,StopReason:StopReasonStop},nil),nil }
   final:=Message{Role:RoleAssistant,StopReason:StopReasonToolUse,ToolCalls:[]ToolCall{{ID:"call-1",Name:"write",Arguments:[]byte("{}")}}}
   return newStaticAssistantStream(final,[]AssistantEvent{
    {Type:AssistantEventError,Message:Message{Role:RoleAssistant,StopReason:StopReasonError,ErrorMessage:"fixture failure"}},
    {Type:AssistantEventDone,Message:final},
   }),nil
  }},
  Tools:[]ToolDefinition{{Name:"write",Execute:func(context.Context,ToolExecutionContext)(ToolResult,error){calls.Add(1);return ToolResult{},nil}}},
 }).Validate()
 if err!=nil { t.Fatal(err) }
 var terminal error
 _,err=NewEngine().Run(t.Context(),definition,&AgentSnapshot{},[]Message{NewTextMessage(RoleUser,"fixture")},func(event AgentEvent){
  if event.Type==EventMessageEnd && event.Message!=nil && event.Message.Role==RoleAssistant { terminal=event.Err }
 })
 // Error assistant messages are finalized without a Run infrastructure error.
 if err!=nil { t.Fatalf("unexpected Run infrastructure error: %v",err) }
 if calls.Load()!=0 || turns!=1 || terminal==nil { t.Fatalf("terminal error revived: calls=%d turns=%d terminal=%v",calls.Load(),turns,terminal) }
}

func TestTerminalErrorEventRetainsOriginalCause(t *testing.T) {
 cause:=errors.New("fixture original")
 definition,err:= (AgentDefinition{Model:staticModel{streamFn:func(context.Context,ModelRequest)(AssistantStream,error){
  return newStaticAssistantStream(Message{Role:RoleAssistant,StopReason:StopReasonStop},[]AssistantEvent{{Type:AssistantEventError,Err:cause}}),nil
 }}}).Validate()
 if err!=nil { t.Fatal(err) }
 var terminal error
 _,_,err=NewEngine().generateAssistant(t.Context(),definition,&AgentSnapshot{},nil,func(event AgentEvent){
  if event.Type==EventMessageEnd { terminal=event.Err }
 })
 if err!=nil || !errors.Is(terminal,cause) { t.Fatalf("cause lost: %v %v",terminal,err) }
}

func TestTerminalErrorEventConsistentFailureAndNormalDoneUnchanged(t *testing.T) {
 for _,tc:=range []struct{event AssistantEventType;reason StopReason;message string}{
  {AssistantEventError,StopReasonError,"fixture failure"},
  {AssistantEventError,StopReasonAborted,"fixture abort"},
  {AssistantEventDone,StopReasonStop,""},
 } {
  final:=Message{Role:RoleAssistant,StopReason:tc.reason,ErrorMessage:tc.message}
  definition,err:= (AgentDefinition{Model:staticModel{streamFn:func(context.Context,ModelRequest)(AssistantStream,error){
   return newStaticAssistantStream(final,[]AssistantEvent{{Type:tc.event,Message:final}}),nil
  }}}).Validate()
  if err!=nil { t.Fatal(err) }
  var terminal error
  got,_,err:=NewEngine().generateAssistant(t.Context(),definition,&AgentSnapshot{},nil,func(event AgentEvent){
   if event.Type==EventMessageEnd { terminal=event.Err }
  })
  if err!=nil || terminal!=nil || got.StopReason!=tc.reason || got.ErrorMessage!=tc.message { t.Fatalf("consistent terminal changed: %+v terminal=%v err=%v",got,terminal,err) }
 }
}
