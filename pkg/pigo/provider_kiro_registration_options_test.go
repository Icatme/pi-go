package pigo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestKiroAutoHTTPExplicitSelectionAndNoRetries(t *testing.T) {
	for _, simple := range []bool{false, true} {
		for _, scenario := range []string{"default-auto", "optin-auto", "optin-sse", "websocket", "cached-websocket", "transport-error"} {
			t.Run(fmt.Sprintf("%t/%s", simple, scenario), func(t *testing.T) {
				config, model := kiroRegistrationFixture(t)
				calls := 0
				original := config.RoundTrip
				config.RoundTrip = func(request *http.Request) (*http.Response, error) {
					calls++
					if scenario == "transport-error" {
						return nil, errors.New("PRIVATE_EXECUTOR_ERROR")
					}
					return original(request)
				}
				var options []KiroBufferedOption
				if scenario != "default-auto" {
					options = []KiroBufferedOption{WithKiroAutoHTTP()}
				}
				if err := RegisterKiroBufferedModuleWithOptions(t.Name(), config, options...); err != nil {
					t.Fatal(err)
				}
				defer UnregisterAPIModules(t.Name())
				transport := TransportAuto
				switch scenario {
				case "optin-sse":
					transport = TransportSSE
				case "websocket":
					transport = TransportWebSocket
				case "cached-websocket":
					transport = TransportWebSocketCached
				}
				conversation := Context{Messages: []Message{UserMessage{Content: "hello"}}}
				var stream *AssistantMessageEventStream
				if simple {
					stream = StreamSimple(model, conversation, SimpleStreamOptions{RequestContext: context.Background(), Transport: transport})
				} else {
					stream = Stream(model, conversation, ProviderStreamOptions{RequestContext: context.Background(), Transport: transport})
				}
				for range stream.Events() {
				}
				result := stream.Result()
				stream.Close()
				expectedCalls := 0
				if scenario == "optin-auto" || scenario == "optin-sse" || scenario == "transport-error" {
					expectedCalls = 1
				}
				expectedStop := StopReasonError
				if scenario == "optin-auto" || scenario == "optin-sse" {
					expectedStop = StopReasonStop
				}
				if calls != expectedCalls || result.StopReason != expectedStop || strings.Contains(result.ErrorMessage, "PRIVATE") {
					t.Fatal("selection/retry/error", calls, result.StopReason)
				}
			})
		}
	}
}

func TestKiroBufferedOptionsValidationAndSecretOwnership(t *testing.T) {
	affinity := KiroSessionAffinity{Tenant: "tenant", Key: bytes.Repeat([]byte("K"), 32)}
	option := WithKiroSessionAffinity(affinity)
	encoded, err := json.Marshal(option)
	if err != nil || string(encoded) != "{}" || strings.Contains(fmt.Sprintf("%v %#v", option, option), "KKKK") {
		t.Fatal("option secret leak")
	}
	affinity.Key[0] = 'X'
	if option.affinity.Key[0] != 'K' {
		t.Fatal("option creation aliases key")
	}
	for _, options := range [][]KiroBufferedOption{{{}}, {WithKiroAutoHTTP(), WithKiroAutoHTTP()}, {option, option}, {WithKiroAutoHTTP(), option, WithKiroAutoHTTP()}, {WithKiroSessionAffinity(KiroSessionAffinity{})}} {
		config, _ := kiroRegistrationFixture(t)
		if err := RegisterKiroBufferedModuleWithOptions(t.Name(), config, options...); err != errKiroRegistration || resolveAPIModule(config.API) != nil {
			t.Fatal("invalid option registered")
		}
	}
	config, model := kiroRegistrationFixture(t)
	requestIDs := config.RequestIDs
	config.RequestIDs = func(ctx context.Context) (KiroRequestIDs, error) {
		ids, err := requestIDs(ctx)
		ids.ConversationID = ""
		return ids, err
	}
	expectedID, _ := kiroSessionConversationID(config, *option.affinity, "session")
	calls := 0
	executor := config.RoundTrip
	config.RoundTrip = func(r *http.Request) (*http.Response, error) {
		calls++
		var envelope struct {
			ConversationState struct {
				ConversationID string `json:"conversationId"`
			} `json:"conversationState"`
		}
		if json.NewDecoder(r.Body).Decode(&envelope) != nil || envelope.ConversationState.ConversationID != expectedID {
			t.Error("registered affinity aliases option")
		}
		return executor(r)
	}
	if err := RegisterKiroBufferedModuleWithOptions(t.Name(), config, option, WithKiroAutoHTTP()); err != nil {
		t.Fatal(err)
	}
	defer UnregisterAPIModules(t.Name())
	// Even a same-package test mutating the option cannot affect registered copy.
	option.affinity.Key[0] = 'Y'
	stream := StreamSimple(model, Context{Messages: []Message{UserMessage{Content: "hello"}}}, SimpleStreamOptions{RequestContext: context.Background(), SessionID: "session", Transport: TransportAuto})
	for range stream.Events() {
	}
	result := stream.Result()
	stream.Close()
	if result.StopReason != StopReasonStop || calls != 1 {
		t.Fatal("composed options")
	}
}

func TestKiroAutoHTTPDoesNotRelaxOtherHostOptions(t *testing.T) {
	config, model := kiroRegistrationFixture(t)
	calls := 0
	config.RoundTrip = func(*http.Request) (*http.Response, error) { calls++; return nil, nil }
	if err := RegisterKiroBufferedModuleWithOptions(t.Name(), config, WithKiroAutoHTTP()); err != nil {
		t.Fatal(err)
	}
	defer UnregisterAPIModules(t.Name())
	for _, options := range []SimpleStreamOptions{{RequestContext: context.Background(), Transport: TransportAuto, APIKey: "PRIVATE"}, {RequestContext: context.Background(), Transport: TransportAuto, HTTPClient: &http.Client{}}, {RequestContext: context.Background(), Transport: TransportAuto, SessionID: "not-opted-in"}} {
		stream := StreamSimple(model, Context{Messages: []Message{UserMessage{Content: "hello"}}}, options)
		for range stream.Events() {
		}
		result := stream.Result()
		stream.Close()
		if result.StopReason != StopReasonError || calls != 0 {
			t.Fatal("unrelated guard relaxed")
		}
	}
}
