package pigo

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type kiroFixtureRoundTripper func(*http.Request) (*http.Response,error)
func (f kiroFixtureRoundTripper) RoundTrip(r *http.Request) (*http.Response,error) {return f(r)}

func kiroHTTPRequestFixture() kiroHTTPRequestOptions {
	id,limits:=kiroRequestFixture()
	return kiroHTTPRequestOptions{Region:"us-east-1",Identity:id,Limits:limits,UserAgent:"pi-go/offline-fixture",InvocationID:"01234567-89ab-cdef-0123-456789abcdef"}
}

func TestKiroHTTPRequestAssembleAndFakeResponse(t *testing.T) {
	options:=kiroHTTPRequestFixture()
	conversation:=Context{Messages:[]Message{UserMessage{Content:"hello fixture"}}}
	ctx:=context.Background()
	request,err:=newKiroOfflineHTTPRequest(ctx,conversation,nil,options)
	if err!=nil {t.Fatal(err)}
	// This transport never delegates to http.DefaultTransport or a real client.
	calls:=0
	transport:=kiroFixtureRoundTripper(func(r *http.Request)(*http.Response,error){
		calls++
		if r.Method!="POST" || r.URL.String()!="https://runtime.us-east-1.kiro.dev/generateAssistantResponse" || r.Context()!=ctx || r.Header.Get("User-Agent")!="pi-go/offline-fixture" || r.Header.Get("X-Amz-User-Agent")!="pi-go/offline-fixture" || r.Header.Get("Accept")!="application/vnd.amazon.eventstream" || r.Header.Get("Authorization")!="" || r.Header.Get("Tokentype")!="" {t.Fatal("HTTP fixture contract changed")}
		body,err:=io.ReadAll(r.Body);if err!=nil {t.Fatal(err)}
		defer r.Body.Close()
		expected,err:=encodeKiroOfflineRequest(conversation,nil,options.Identity,options.Limits)
		if err!=nil || !bytes.Equal(body,expected) || r.ContentLength!=int64(len(body)) {t.Fatal("body changed after projection")}
		wire:=kiroOfflineJoin(kiroOfflineEvent("assistantResponseEvent",`{"content":"fixture answer"}`),kiroOfflineEvent("metadataEvent",`{"stopReason":"END_TURN"}`))
		return &http.Response{StatusCode:200,Header:http.Header{"Content-Type":[]string{"application/vnd.amazon.eventstream"}},Body:io.NopCloser(bytes.NewReader(wire)),Request:r},nil
	})
	response,err:=transport.RoundTrip(request)
	if err!=nil {t.Fatal(err)}
	defer response.Body.Close()
	result,err:=collectKiroOfflineResponse(ctx,response.Body,kiroOfflineTestLimits())
	if err!=nil || calls!=1 || len(result.Parts)!=1 || result.Parts[0].Text!="fixture answer" || result.StopReason!=StopReasonStop {t.Fatalf("fixture response: %#v %v",result,err)}
}

func TestKiroHTTPRequestRejectsInvalidConfiguration(t *testing.T) {
	conversation:=Context{Messages:[]Message{UserMessage{Content:"hello"}}}
	for _,region:=range []string{"", "US-EAST-1", "us-east-1.evil.invalid", "us-east-1:443", "us-east-1/", "us-east-1?x=y", "us-east-1#fragment", "us-east-1@evil", " us-east-1", "us-east-1\r\nHost: evil"} {
		options:=kiroHTTPRequestFixture();options.Region=region
		if req,err:=newKiroOfflineHTTPRequest(context.Background(),conversation,nil,options);err==nil || req!=nil {t.Fatalf("invalid region accepted: %q",region)}
	}
	for _,mutate:=range []func(*kiroHTTPRequestOptions){
		func(o *kiroHTTPRequestOptions){o.UserAgent="bad\nheader"},
		func(o *kiroHTTPRequestOptions){o.InvocationID="bad"},
		func(o *kiroHTTPRequestOptions){o.Identity.Origin=""},
		func(o *kiroHTTPRequestOptions){o.Limits.WireBytes=2},
	} {
		options:=kiroHTTPRequestFixture();mutate(&options)
		if req,err:=newKiroOfflineHTTPRequest(context.Background(),conversation,nil,options);err==nil || req!=nil {t.Fatal("invalid options returned request")}
	}
	options:=kiroHTTPRequestFixture()
	if req,err:=newKiroOfflineHTTPRequest(nil,conversation,nil,options);err==nil || req!=nil {t.Fatal("nil context accepted")}
	cancelled,cancel:=context.WithCancel(context.Background());cancel()
	if req,err:=newKiroOfflineHTTPRequest(cancelled,conversation,nil,options);err==nil || req!=nil {t.Fatal("cancelled construction accepted")}
}

func TestKiroHTTPRequestOwnershipAndContext(t *testing.T) {
	ctx,cancel:=context.WithCancel(context.Background());defer cancel()
	options:=kiroHTTPRequestFixture()
	conversation,schemas:=kiroToolProjectionFixture()
	first,err:=newKiroOfflineHTTPRequest(ctx,conversation,schemas,options);if err!=nil {t.Fatal(err)}
	second,err:=newKiroOfflineHTTPRequest(ctx,conversation,schemas,options);if err!=nil {t.Fatal(err)}
	first.Header.Set("User-Agent","mutated")
	if second.Header.Get("User-Agent")!="pi-go/offline-fixture" {t.Fatal("request headers alias")}
	schemas["lookup"][0]='!'
	body,err:=io.ReadAll(second.Body);if err!=nil {t.Fatal(err)};second.Body.Close();first.Body.Close()
	if !bytes.Contains(body,[]byte(`9007199254740993`)) || !bytes.Contains(body,[]byte(`"inputSchema":{"json":{"type":"object"`)) {t.Fatal("request body aliases schema or loses precision")}
	cancel()
	if second.Context().Err()!=context.Canceled {t.Fatal("context detached")}
}

func TestKiroHTTPRequestRegionLabelBoundary(t *testing.T) {
 options:=kiroHTTPRequestFixture()
 conversation:=Context{Messages:[]Message{UserMessage{Content:"fixture"}}}
 options.Region="us-"+strings.Repeat("a",58)+"-1"
 req,err:=newKiroOfflineHTTPRequest(context.Background(),conversation,nil,options)
 if err!=nil || req==nil {t.Fatal("63-byte region label rejected")}
 req.Body.Close()
 options.Region="us-"+strings.Repeat("a",59)+"-1"
 if req,err:=newKiroOfflineHTTPRequest(context.Background(),conversation,nil,options);err==nil || req!=nil {t.Fatal("64-byte region label accepted")}
}
