package pigo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"testing"
)

type kiroAttemptBody struct {
	reader io.Reader
	reads, closes, bytesRead int
	closeErr error
	onClose func()
}
func (b *kiroAttemptBody) Read(p []byte)(int,error){b.reads++;n,err:=b.reader.Read(p);b.bytesRead+=n;return n,err}
func (b *kiroAttemptBody) Close()error{b.closes++;if b.onClose!=nil {b.onClose()};return b.closeErr}

func kiroAttemptWire() []byte {
	return kiroOfflineJoin(kiroOfflineEvent("assistantResponseEvent",`{"content":"fixture answer"}`),kiroOfflineEvent("metadataEvent",`{"stopReason":"END_TURN"}`))
}
func kiroAttemptLimits() kiroHTTPResponseLimits {return kiroHTTPResponseLimits{WireBytes:8192,Stream:kiroOfflineTestLimits()}}
func kiroAttemptResponse(body io.ReadCloser) *http.Response {return &http.Response{StatusCode:200,ContentLength:-1,Header:http.Header{"Content-Type":[]string{"application/vnd.amazon.eventstream"}},Body:body}}
func kiroRunAttemptFixture(ctx context.Context, transport func(*http.Request)(*http.Response,error), limits kiroHTTPResponseLimits)(kiroOfflineResponse,error){
	return runKiroInjectedAttempt(ctx,Context{Messages:[]Message{UserMessage{Content:"fixture"}}},nil,kiroHTTPRequestFixture(),kiroCredentialBearer,"LOCAL_ONLY_TRANSPORT_FAKE",transport,limits)
}

func TestKiroInjectedAttemptSuccessExactlyOnceAndClose(t *testing.T) {
	wire:=kiroAttemptWire();body:=&kiroAttemptBody{reader:bytes.NewReader(wire)};calls:=0
	result,err:=kiroRunAttemptFixture(context.Background(),func(req *http.Request)(*http.Response,error){
		calls++
		if req.Header.Get("Authorization")!="Bearer LOCAL_ONLY_TRANSPORT_FAKE" || req.URL.Host!="runtime.us-east-1.kiro.dev" {t.Fatal("fixture request mismatch")}
		response:=kiroAttemptResponse(body);response.ContentLength=int64(len(wire));return response,nil
	},kiroAttemptLimits())
	if err!=nil || calls!=1 || body.closes!=1 || len(result.Parts)!=1 || result.Parts[0].Text!="fixture answer" {t.Fatal("single injected attempt did not complete")}
}

func TestKiroInjectedAttemptRejectsRedirectAndMetadataBeforeRead(t *testing.T) {
	for _,mutate:=range []func(*http.Response){
		func(r *http.Response){r.StatusCode=307;r.Header.Set("Location","https://other.invalid/")},
		func(r *http.Response){r.StatusCode=308},
		func(r *http.Response){r.StatusCode=401},
		func(r *http.Response){r.StatusCode=429},
		func(r *http.Response){r.StatusCode=503},
		func(r *http.Response){r.Header.Set("Content-Type","application/json")},
		func(r *http.Response){r.Header.Del("Content-Type")},
		func(r *http.Response){r.Header["content-type"]=[]string{"application/json"}},
		func(r *http.Response){r.Header.Set("Content-Encoding","gzip")},
		func(r *http.Response){r.Uncompressed=true},
		func(r *http.Response){r.ContentLength=8193},
		func(r *http.Response){r.ContentLength=-2},
	} {
		body:=&kiroAttemptBody{reader:bytes.NewReader(kiroAttemptWire())};calls:=0
		result,err:=kiroRunAttemptFixture(context.Background(),func(*http.Request)(*http.Response,error){calls++;r:=kiroAttemptResponse(body);mutate(r);return r,nil},kiroAttemptLimits())
		if err==nil || !reflect.DeepEqual(result,kiroOfflineResponse{}) || calls!=1 || body.reads!=0 || body.closes!=1 {t.Fatal("rejected response read/retried/leaked result")}
	}
}

func TestKiroInjectedAttemptErrorsCloseAndClear(t *testing.T) {
	for _,mode:=range []string{"transport", "nil-response", "nil-body", "close", "truncated", "content-length"} {
		wire:=kiroAttemptWire();if mode=="truncated" {wire=wire[:len(wire)-1]}
		body:=&kiroAttemptBody{reader:bytes.NewReader(wire)}
		if mode=="close" {body.closeErr=errors.New("LOCAL_ONLY_SECRET_IN_CLOSE_ERROR")}
		calls:=0
		result,err:=kiroRunAttemptFixture(context.Background(),func(*http.Request)(*http.Response,error){
			calls++;r:=kiroAttemptResponse(body)
			switch mode {
			case "transport": return r,errors.New("LOCAL_ONLY_SECRET_IN_TRANSPORT_ERROR")
			case "nil-response":return nil,nil
			case "nil-body":r.Body=nil
			case "content-length":r.ContentLength=1
			}
			return r,nil
		},kiroAttemptLimits())
		if err==nil || err.Error()!="kiro: injected attempt failed" || !reflect.DeepEqual(result,kiroOfflineResponse{}) || calls!=1 {t.Fatal("failure leaked result/error text or retried")}
		expectedCloses:=1;if mode=="nil-response" || mode=="nil-body" {expectedCloses=0}
		if body.closes!=expectedCloses {t.Fatal("response ownership mismatch")}
	}
}

func TestKiroInjectedAttemptWireLimitNotCompletionProof(t *testing.T) {
	wire:=kiroAttemptWire()
	for _,budget:=range []int64{int64(len(wire)),int64(len(wire)-1)} {
		body:=&kiroAttemptBody{reader:bytes.NewReader(wire)};limits:=kiroAttemptLimits();limits.WireBytes=budget
		result,err:=kiroRunAttemptFixture(context.Background(),func(*http.Request)(*http.Response,error){return kiroAttemptResponse(body),nil},limits)
		if budget==int64(len(wire)) {if err!=nil || len(result.Parts)!=1 {t.Fatal("exact byte budget rejected")}} else if err==nil || !reflect.DeepEqual(result,kiroOfflineResponse{}) {t.Fatal("limit-created EOF authorized result")}
		if body.closes!=1 || int64(body.bytesRead)>budget+1 {t.Fatal("limited body ownership/byte budget failed")}
	}
}

func TestKiroInjectedAttemptCancellationAndNoImplicitTransport(t *testing.T) {
	for _,phase:=range []string{"before","after-transport"} {
		ctx,cancel:=context.WithCancel(context.Background());body:=&kiroAttemptBody{reader:bytes.NewReader(kiroAttemptWire())};calls:=0
		if phase=="before" {cancel()}
		result,err:=kiroRunAttemptFixture(ctx,func(req *http.Request)(*http.Response,error){calls++;if req.Context()!=ctx {t.Fatal("context detached")};cancel();return kiroAttemptResponse(body),nil},kiroAttemptLimits());cancel()
		if err==nil || !reflect.DeepEqual(result,kiroOfflineResponse{}) {t.Fatal("cancellation released result")}
		if phase=="before" {if calls!=0 || body.closes!=0 {t.Fatal("pre-cancelled call executed")}} else if calls!=1 || body.reads!=0 || body.closes!=1 {t.Fatal("cancelled response ownership")}
	}
	if result,err:=kiroRunAttemptFixture(context.Background(),nil,kiroAttemptLimits());err==nil || !reflect.DeepEqual(result,kiroOfflineResponse{}) {t.Fatal("nil executor accepted")}
	calls:=0;limits:=kiroAttemptLimits();limits.WireBytes=0
	_,err:=kiroRunAttemptFixture(context.Background(),func(*http.Request)(*http.Response,error){calls++;return nil,nil},limits)
	if err==nil || calls!=0 {t.Fatal("invalid limits admitted execution")}
}

type kiroCancelDuringRead struct {reader io.Reader;cancel context.CancelFunc}
func (r kiroCancelDuringRead) Read(p []byte)(int,error){n,err:=r.reader.Read(p);r.cancel();return n,err}
func TestKiroInjectedAttemptLateCancellationClearsResult(t *testing.T) {
 for _,phase:=range []string{"read","close"} {
  ctx,cancel:=context.WithCancel(context.Background())
  body:=&kiroAttemptBody{reader:bytes.NewReader(kiroAttemptWire())}
  if phase=="read" {body.reader=kiroCancelDuringRead{reader:body.reader,cancel:cancel}} else {body.onClose=cancel}
  result,err:=kiroRunAttemptFixture(ctx,func(*http.Request)(*http.Response,error){return kiroAttemptResponse(body),nil},kiroAttemptLimits())
  cancel()
  if err==nil || !reflect.DeepEqual(result,kiroOfflineResponse{}) || body.closes!=1 {t.Fatal("late cancellation released staged output")}
 }
}
