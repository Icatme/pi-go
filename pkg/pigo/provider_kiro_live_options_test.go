package pigo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestKiroLiveOptionsCopyAffinityAndSelectOnlyHTTP(t *testing.T) {
	c, m := kiroPublicLiveFixture(t)
	base := c.RequestIDs
	c.RequestIDs = func(ctx context.Context) (KiroRequestIDs, error) {
		ids, err := base(ctx)
		ids.ConversationID = ""
		return ids, err
	}
	input := KiroSessionAffinity{Tenant: "tenant", Key: bytes.Repeat([]byte("K"), 32)}
	shared := KiroBufferedConfig{API: c.API, Provider: c.Provider, Region: c.Region, ProfileARN: c.ProfileARN}
	expected, ok := kiroSessionConversationID(shared, input, "session")
	if !ok {
		t.Fatal("expected")
	}
	calls := 0
	original := c.RoundTrip
	c.RoundTrip = func(r *http.Request) (*http.Response, error) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		var wire struct {
			ConversationState struct {
				ConversationID string `json:"conversationId"`
			} `json:"conversationState"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil || wire.ConversationState.ConversationID != expected {
			t.Error("affinity snapshot")
		}
		return original(r)
	}
	if err := RegisterKiroLiveTextModuleWithOptions(t.Name(), c, KiroLiveTextOptions{AutoHTTP: true, SessionAffinity: &input}); err != nil {
		t.Fatal(err)
	}
	defer UnregisterAPIModules(t.Name())
	input.Key[0] = 'X'
	input.Tenant = "mutated"
	for _, transport := range []Transport{TransportAuto, TransportSSE, TransportWebSocket} {
		stream := StreamSimple(m, Context{Messages: []Message{UserMessage{Content: "hi"}}}, SimpleStreamOptions{RequestContext: context.Background(), SessionID: "session", Transport: transport})
		kiroReadHostEvents(t, stream)
		r := kiroReadHostResult(t, stream)
		stream.Close()
		if transport == TransportWebSocket {
			if r.StopReason != StopReasonError {
				t.Fatal("websocket accepted")
			}
		} else if r.StopReason != StopReasonStop {
			t.Fatal("allowed HTTP rejected")
		}
	}
	if calls != 2 {
		t.Fatal("retry or rejected transport call", calls)
	}
}

func TestKiroLiveOptionsInvalidAffinityHasNoRegistration(t *testing.T) {
	for _, input := range []KiroSessionAffinity{{Tenant: "tenant", Key: []byte("short")}, {Tenant: " bad ", Key: bytes.Repeat([]byte("K"), 32)}} {
		c, _ := kiroPublicLiveFixture(t)
		if err := RegisterKiroLiveTextModuleWithOptions(t.Name(), c, KiroLiveTextOptions{SessionAffinity: &input}); err != errKiroLiveRegistration {
			t.Fatal("invalid affinity accepted")
		}
		if GetAPIModule(c.API) != nil {
			t.Fatal("registered invalid config")
		}
	}
}

func TestKiroLiveOptionsDiagnosticFormsExcludeKey(t *testing.T) {
	secret := "NEVER_PRINT_THIS_KEY_1234567890123456"
	affinity := KiroSessionAffinity{Tenant: "tenant", Key: []byte(secret)}
	options := KiroLiveTextOptions{AutoHTTP: true, SessionAffinity: &affinity}
	raw, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{string(raw), fmt.Sprintf("%v", options), fmt.Sprintf("%+v", options), fmt.Sprintf("%#v", options)} {
		if strings.Contains(value, secret) || strings.Contains(value, "NEVER_PRINT") || strings.Contains(value, "78 69 86 69 82") {
			t.Fatal("secret in diagnostic form")
		}
	}
}
