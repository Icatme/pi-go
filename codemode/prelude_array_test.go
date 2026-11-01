package codemode

import (
 "context"
 "encoding/json"
 "sync/atomic"
 "testing"
)

func TestNamedArrayPropertiesCannotDisappearBeforeDispatch(t *testing.T) {
 s:=sandboxForTest(t,DefaultConfig())
 var calls atomic.Int32
 tool:=Tool{Name:"echo",Invoke:func(_ context.Context,call HostCall)(json.RawMessage,error){
  calls.Add(1)
  return call.Arguments,nil
 }}
 for _,key:=range []string{"customer","01","-0","-1","1e0","4294967295","__proto__"} {
  keyJSON,_:=json.Marshal(key)
  code:=`const a=[1];Object.defineProperty(a,`+string(keyJSON)+`,{value:"lost",enumerable:true});try{await tools.echo({values:a});return "accepted"}catch(e){return e.code}`
  result,err:=s.Run(t.Context(),code,RunOptions{Tools:[]Tool{tool}})
  if err!=nil || textOutput(result)!="unsupported_json" || calls.Load()!=0 || len(result.Calls)!=0 {
   t.Fatalf("named property reached host: key=%s result=%+v err=%v calls=%d",key,result,err,calls.Load())
  }
 }
 result,err:=s.Run(t.Context(),`return await tools.echo({values:[null,true,0.1,"中",[]]})`,RunOptions{Tools:[]Tool{tool}})
 if err!=nil || calls.Load()!=1 || textOutput(result)!=`{"values":[null,true,0.1,"中",[]]}` {
  t.Fatalf("dense array changed: %+v %v calls=%d",result,err,calls.Load())
 }
}

func TestNamedArrayPropertiesCannotDisappearFromOutputOrStore(t *testing.T) {
 s:=sandboxForTest(t,DefaultConfig())
 for _,expression:=range []string{`text(a)`,`store("key",a)`} {
  result,err:=s.Run(t.Context(),`const a=[1];a.extra=2;try{`+expression+`;return "accepted"}catch(e){return e.code}`,RunOptions{})
  if err!=nil || textOutput(result)!="unsupported_json" { t.Fatalf("named array accepted: %+v %v",result,err) }
 }
 result,err:=s.Run(t.Context(),`let hits=0;const a=[1];Object.defineProperty(a,"extra",{enumerable:true,get(){hits++;return 2}});try{text(a)}catch(e){return e.code+":"+hits}`,RunOptions{})
 if err!=nil || textOutput(result)!="unsupported_json:0" { t.Fatalf("getter executed: %+v %v",result,err) }
}

func TestInheritedDescriptorValueCannotDisguiseAccessor(t *testing.T) {
 s:=sandboxForTest(t,DefaultConfig())
 for _,initial:=range []string{"[]","{}"} {
  code:=`let hits=0;const a=`+initial+`;Object.defineProperty(a,"0",{enumerable:true,get(){hits++;return "real"}});Object.prototype.value="injected";try{text(a)}catch(e){return e.code+":"+hits}`
  result,err:=s.Run(t.Context(),code,RunOptions{})
  if err!=nil || textOutput(result)!="unsupported_json:0" { t.Fatalf("inherited value disguised getter: %+v %v",result,err) }
 }
}
