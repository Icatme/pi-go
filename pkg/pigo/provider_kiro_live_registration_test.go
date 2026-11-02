package pigo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

func kiroPublicLiveFixture(t *testing.T) (KiroLiveTextConfig, Model) {
	c, m := kiroRegistrationFixture(t)
	return KiroLiveTextConfig{API: c.API, Provider: c.Provider, Region: c.Region, ProfileARN: c.ProfileARN, Origin: c.Origin, UserAgent: c.UserAgent, SystemPromptPolicy: c.SystemPromptPolicy, RequestIDs: c.RequestIDs, Credentials: c.Credentials, RoundTrip: c.RoundTrip}, m
}

func TestKiroPublicLiveRegistrationIncrementalBothRoutes(t *testing.T) {
	for _, simple := range []bool{false, true} {
		t.Run(map[bool]string{false: "full", true: "simple"}[simple], func(t *testing.T) {
			c, m := kiroPublicLiveFixture(t)
			pr, pw := io.Pipe()
			t.Cleanup(func() { pw.Close(); pr.Close() })
			sourceCalls, httpCalls := 0, 0
			source := c.Credentials
			c.Credentials = func(ctx context.Context, s KiroCredentialScope) (KiroScopedCredential, error) {
				sourceCalls++
				return source(ctx, s)
			}
			body := &kiroAttemptBody{reader: pr}
			c.RoundTrip = func(r *http.Request) (*http.Response, error) {
				httpCalls++
				stop := context.AfterFunc(r.Context(), func() { pr.CloseWithError(context.Canceled) })
				body.onClose = func() { stop(); pr.Close() }
				return kiroAttemptResponse(body), nil
			}
			if err := RegisterKiroLiveTextModule(t.Name(), c); err != nil {
				t.Fatal(err)
			}
			defer UnregisterAPIModules(t.Name())
			if sourceCalls+httpCalls != 0 {
				t.Fatal("registration callbacks")
			}
			if GetAPIModule(m.API).Capabilities.CustomTools != CapabilityUnsupported {
				t.Fatal("tools capability")
			}
			conversation := Context{Messages: []Message{UserMessage{Content: "hello"}}}
			var stream *AssistantMessageEventStream
			if simple {
				stream = StreamSimple(m, conversation, SimpleStreamOptions{RequestContext: context.Background(), Transport: TransportSSE})
			} else {
				stream = Stream(m, conversation, ProviderStreamOptions{RequestContext: context.Background(), Transport: TransportSSE})
			}
			defer stream.Close()
			go func() { pw.Write(kiroOfflineEvent("assistantResponseEvent", `{"content":"live"}`)) }()
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			for {
				select {
				case e, ok := <-stream.Events():
					if !ok {
						t.Fatal("closed before live delta")
					}
					if e.Type == AssistantMessageEventTextDelta {
						if e.Delta != "live" {
							t.Fatal("delta")
						}
						goto delivered
					}
				case <-timer.C:
					t.Fatal("buffered instead of live")
				}
			}
		delivered:
			go func() { pw.Write(kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`)); pw.Close() }()
			events := kiroReadHostEvents(t, stream)
			result := kiroReadHostResult(t, stream)
			done := 0
			for _, e := range events {
				if e.Type == AssistantMessageEventDone {
					done++
				}
			}
			if done != 1 || result.StopReason != StopReasonStop || sourceCalls != 1 || httpCalls != 1 || body.closes != 1 {
				t.Fatal("completion")
			}
		})
	}
}

func TestKiroPublicLiveRegistrationRejectsBeforeCredentials(t *testing.T) {
	for _, mode := range []string{"tools", "initial-tools", "auto", "websocket", "session", "api-key", "default-system", "model-url"} {
		t.Run(mode, func(t *testing.T) {
			c, m := kiroPublicLiveFixture(t)
			calls := 0
			c.Credentials = func(context.Context, KiroCredentialScope) (KiroScopedCredential, error) {
				calls++
				return KiroScopedCredential{}, errors.New("must not call")
			}
			c.RoundTrip = func(*http.Request) (*http.Response, error) { calls++; return nil, nil }
			if err := RegisterKiroLiveTextModule(t.Name(), c); err != nil {
				t.Fatal(err)
			}
			defer UnregisterAPIModules(t.Name())
			conversation := Context{Messages: []Message{UserMessage{Content: "hello"}}}
			o := SimpleStreamOptions{RequestContext: context.Background(), Transport: TransportSSE}
			switch mode {
			case "tools":
				conversation.Tools = []Tool{{Name: "lookup"}}
			case "initial-tools":
				conversation.Messages = append([]Message{SystemMessage{ToolsAdded: []Tool{{Name: "lookup"}}}}, conversation.Messages...)
			case "auto":
				o.Transport = TransportAuto
			case "websocket":
				o.Transport = TransportWebSocket
			case "session":
				o.SessionID = "session"
			case "api-key":
				o.APIKey = "PRIVATE"
			case "default-system":
				conversation.SystemPrompt = "private system"
			case "model-url":
				m.BaseURL = "https://unused.invalid"
			}
			stream := StreamSimple(m, conversation, o)
			defer stream.Close()
			kiroReadHostEvents(t, stream)
			r := kiroReadHostResult(t, stream)
			if r.StopReason != StopReasonError || calls != 0 {
				t.Fatal("rejected request reached callback")
			}
		})
	}
}

func TestKiroPublicLiveRegistrationLateFailureNoFallback(t *testing.T) {
	c, m := kiroPublicLiveFixture(t)
	calls := 0
	body := &kiroAttemptBody{reader: bytes.NewReader(kiroOfflineJoin(kiroOfflineEvent("assistantResponseEvent", `{"content":"preview"}`), kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`))), closeErr: errors.New("PRIVATE")}
	c.RoundTrip = func(*http.Request) (*http.Response, error) { calls++; return kiroAttemptResponse(body), nil }
	if err := RegisterKiroLiveTextModule(t.Name(), c); err != nil {
		t.Fatal(err)
	}
	defer UnregisterAPIModules(t.Name())
	stream := StreamSimple(m, Context{Messages: []Message{UserMessage{Content: "hello"}}}, SimpleStreamOptions{RequestContext: context.Background(), Transport: TransportSSE})
	defer stream.Close()
	for _, e := range kiroReadHostEvents(t, stream) {
		if e.Type == AssistantMessageEventDone || e.Type == AssistantMessageEventToolCallEnd {
			t.Fatal("executable success")
		}
	}
	r := kiroReadHostResult(t, stream)
	if r.StopReason != StopReasonError || len(r.Content) != 0 || calls != 1 || body.closes != 1 {
		t.Fatal("late failure")
	}
}
