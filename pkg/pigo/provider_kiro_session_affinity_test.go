package pigo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestKiroSessionAffinityStableVectorAndScopeIsolation(t *testing.T) {
	config := KiroBufferedConfig{API: "api-a", Provider: "provider-a", Region: "us-east-1", ProfileARN: "profile-a"}
	affinity := KiroSessionAffinity{Tenant: "tenant-a", Key: bytes.Repeat([]byte("K"), 32)}
	expected := "pi-72fc91632b693f37df9797207ab16799d1a57523d70de75fc3c584d09c5c7d79" // independently computed with Python hmac/struct
	actual, ok := kiroSessionConversationID(config, affinity, "session-a")
	if !ok || actual != expected {
		t.Fatal("vector")
	}
	for _, mutate := range []func(*KiroBufferedConfig, *KiroSessionAffinity){
		func(c *KiroBufferedConfig, a *KiroSessionAffinity) { c.API = "api-b" }, func(c *KiroBufferedConfig, a *KiroSessionAffinity) { c.Provider = "provider-b" }, func(c *KiroBufferedConfig, a *KiroSessionAffinity) { c.Region = "us-west-2" }, func(c *KiroBufferedConfig, a *KiroSessionAffinity) { c.ProfileARN = "profile-b" }, func(c *KiroBufferedConfig, a *KiroSessionAffinity) { a.Tenant = "tenant-b" }, func(c *KiroBufferedConfig, a *KiroSessionAffinity) { a.Key = bytes.Repeat([]byte("L"), 32) },
	} {
		c, a := config, affinity
		mutate(&c, &a)
		id, ok := kiroSessionConversationID(c, a, "session-a")
		if !ok || id == expected {
			t.Fatal("scope collision")
		}
	}
	if id, _ := kiroSessionConversationID(config, affinity, "session-b"); id == expected {
		t.Fatal("session collision")
	}
	left, right := config, config
	left.ProfileARN = "ab"
	right.ProfileARN = "a"
	a, _ := kiroSessionConversationID(left, affinity, "c")
	b, _ := kiroSessionConversationID(right, affinity, "bc")
	if a == b {
		t.Fatal("ambiguous concatenation")
	}
	raw, _ := json.Marshal(affinity)
	if strings.Contains(string(raw), "KKKK") || strings.Contains(fmt.Sprintf("%v %#v", affinity, affinity), "KKKK") {
		t.Fatal("key formatting leak")
	}
}

func TestKiroSessionAffinityConcurrentDispatchAndKeySnapshot(t *testing.T) {
	config, model := kiroRegistrationFixture(t)
	affinity := KiroSessionAffinity{Tenant: "tenant", Key: bytes.Repeat([]byte("K"), 32)}
	expectedA, _ := kiroSessionConversationID(config, affinity, "session-a")
	expectedB, _ := kiroSessionConversationID(config, affinity, "session-b")
	var sequence, credentials atomic.Int64
	config.RequestIDs = func(context.Context) (KiroRequestIDs, error) {
		return KiroRequestIDs{InvocationID: fmt.Sprintf("%08x-0000-0000-0000-000000000000", sequence.Add(1))}, nil
	}
	originalCredentials, originalExecutor := config.Credentials, config.RoundTrip
	config.Credentials = func(ctx context.Context, scope KiroCredentialScope) (KiroScopedCredential, error) {
		credentials.Add(1)
		return originalCredentials(ctx, scope)
	}
	counts := map[string]int{}
	invocations := map[string]bool{}
	var mu sync.Mutex
	config.RoundTrip = func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		if bytes.Contains(body, []byte("session-a")) || bytes.Contains(body, []byte("session-b")) {
			t.Error("raw session serialized")
		}
		var envelope struct {
			ConversationState struct {
				ConversationID string `json:"conversationId"`
			} `json:"conversationState"`
		}
		if json.Unmarshal(body, &envelope) != nil {
			t.Error("body")
		}
		mu.Lock()
		counts[envelope.ConversationState.ConversationID]++
		invocation := request.Header.Get("Amz-Sdk-Invocation-Id")
		if invocations[invocation] {
			t.Error("duplicate invocation")
		}
		invocations[invocation] = true
		mu.Unlock()
		return originalExecutor(request)
	}
	if err := RegisterKiroSessionBufferedModule(t.Name(), config, affinity); err != nil {
		t.Fatal(err)
	}
	defer UnregisterAPIModules(t.Name())
	affinity.Key[0] = 'X' // registered material must not alias caller's key
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			session := "session-a"
			if i%2 != 0 {
				session = "session-b"
			}
			var s *AssistantMessageEventStream
			conversation := Context{Messages: []Message{UserMessage{Content: "hello"}}}
			if i < 4 {
				s = Stream(model, conversation, ProviderStreamOptions{RequestContext: context.Background(), SessionID: session})
			} else {
				s = StreamSimple(model, conversation, SimpleStreamOptions{RequestContext: context.Background(), SessionID: session})
			}
			for range s.Events() {
			}
			result := s.Result()
			s.Close()
			if result.StopReason != StopReasonStop {
				t.Error("dispatch failed", result.ErrorMessage)
			}
		})
	}
	wg.Wait()
	if counts[expectedA] != 4 || counts[expectedB] != 4 || len(counts) != 2 || credentials.Load() != 8 || sequence.Load() != 8 {
		t.Fatal("affinity/concurrency/snapshot")
	}
}

func TestKiroSessionAffinityConflictsAndInvalidIDsFailClosed(t *testing.T) {
	for _, scenario := range []string{"empty", "whitespace", "control", "oversize", "conflict", "default-mode"} {
		t.Run(scenario, func(t *testing.T) {
			config, model := kiroRegistrationFixture(t)
			ids, credentials, httpCalls := 0, 0, 0
			options := SimpleStreamOptions{RequestContext: context.Background(), SessionID: "session"}
			switch scenario {
			case "empty":
				options.SessionID = ""
			case "whitespace":
				options.SessionID = " session"
			case "control":
				options.SessionID = "session\n"
			case "oversize":
				options.SessionID = strings.Repeat("x", 257)
			}
			config.RequestIDs = func(context.Context) (KiroRequestIDs, error) {
				ids++
				return KiroRequestIDs{ConversationID: "conflicting-caller-id", InvocationID: "00000000-0000-0000-0000-000000000000"}, nil
			}
			config.Credentials = func(context.Context, KiroCredentialScope) (KiroScopedCredential, error) {
				credentials++
				return KiroScopedCredential{}, nil
			}
			config.RoundTrip = func(*http.Request) (*http.Response, error) { httpCalls++; return nil, nil }
			var err error
			if scenario == "default-mode" {
				err = RegisterKiroBufferedModule(t.Name(), config)
			} else {
				err = RegisterKiroSessionBufferedModule(t.Name(), config, KiroSessionAffinity{Tenant: "tenant", Key: bytes.Repeat([]byte("K"), 32)})
			}
			if err != nil {
				t.Fatal(err)
			}
			defer UnregisterAPIModules(t.Name())
			s := StreamSimple(model, Context{Messages: []Message{UserMessage{Content: "hello"}}}, options)
			for range s.Events() {
			}
			result := s.Result()
			s.Close()
			expectedIDs := 0
			if scenario == "conflict" {
				expectedIDs = 1
			}
			if result.StopReason != StopReasonError || credentials != 0 || httpCalls != 0 || ids != expectedIDs || strings.Contains(result.ErrorMessage, "session") {
				t.Fatal("failure side effect/leak")
			}
		})
	}
}

func TestKiroSessionAffinityRegistrationRequiresExplicitMaterial(t *testing.T) {
	for _, affinity := range []KiroSessionAffinity{{}, {Tenant: "tenant", Key: make([]byte, 31)}, {Tenant: "tenant", Key: make([]byte, 65)}, {Tenant: " bad", Key: make([]byte, 32)}} {
		config, _ := kiroRegistrationFixture(t)
		if err := RegisterKiroSessionBufferedModule(t.Name(), config, affinity); err != errKiroRegistration || resolveAPIModule(config.API) != nil {
			t.Fatal("invalid affinity registered")
		}
	}
}
