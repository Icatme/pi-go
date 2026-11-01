package pigo

import (
 "bytes"
 "encoding/hex"
 "encoding/json"
 "fmt"
 "io"
 "os"
 "testing"
)

// Consume independent Python-produced binary fixtures rather than relying only
// on the Go test encoder. This is framing verification, not event semantics.
func TestKiroIndependentWireCorpus(t *testing.T) {
 raw,err:=os.ReadFile("testdata/kiro/synthetic-wire.json")
 if err!=nil { t.Fatal(err) }
 var corpus struct {
  Version int `json:"schema_version"`
  Cases []struct {
   Name string `json:"name"`
   Frames []struct {
    Headers map[string]string `json:"headers"`
    Payload string `json:"payload_utf8"`
    Hex string `json:"wire_hex"`
   } `json:"frames"`
  } `json:"cases"`
 }
 if err:=json.Unmarshal(raw,&corpus);err!=nil { t.Fatal(err) }
 if corpus.Version!=1 || len(corpus.Cases)==0 { t.Fatal("unsupported or empty corpus") }
 for _,c:=range corpus.Cases {
  for index,f:=range c.Frames {
   t.Run(fmt.Sprintf("%s/%d",c.Name,index),func(t *testing.T){
    wire,err:=hex.DecodeString(f.Hex)
    if err!=nil || len(wire)<16 { t.Fatal("invalid corpus hex") }
    verify:=func(reader io.Reader){
     t.Helper()
     d,err:=newKiroEventStreamDecoder(reader,len(wire))
     if err!=nil { t.Fatal(err) }
     got,err:=d.next()
     if err!=nil || string(got.Payload)!=f.Payload || len(got.Headers)!=len(f.Headers) { t.Fatalf("frame mismatch: %#v, %v",got,err) }
     for name,want:=range f.Headers {
      header,ok:=got.Headers[name]
      if !ok || header.Type!=7 || header.Value!=want { t.Fatalf("header mismatch: %s",name) }
     }
     if _,err:=d.next();err!=io.EOF { t.Fatalf("trailing bytes: %v",err) }
    }
    verify(kiroFragmentReader{bytes.NewReader(wire)})
    for split:=0;split<=len(wire);split++ {
     verify(io.MultiReader(bytes.NewReader(wire[:split]),bytes.NewReader(wire[split:])))
    }
    for end:=0;end<len(wire);end++ {
     d,_:=newKiroEventStreamDecoder(bytes.NewReader(wire[:end]),len(wire))
     if _,err:=d.next();err==nil { t.Fatalf("accepted truncated prefix %d",end) }
    }
    for offset:=range wire {
     damaged:=append([]byte(nil),wire...)
     damaged[offset]^=1
     d,_:=newKiroEventStreamDecoder(bytes.NewReader(damaged),len(wire))
     if _,err:=d.next();err==nil { t.Fatalf("accepted one-bit corruption at %d",offset) }
    }
   })
  }
 }
}
