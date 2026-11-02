package pigo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestKiroAuthoredRegistrationExplicitRequestPath(t *testing.T) {
	for _, incremental := range []bool{false, true} {
		for _, simple := range []bool{false, true} {
			for _, scenario := range []string{"strict-reject", "authored", "edited", "restored", "rounded", "conflict", "cancelled"} {
				t.Run(fmt.Sprintf("%t/%t/%s", incremental, simple, scenario), func(t *testing.T) {
					config, model := kiroRegistrationFixture(t)
					expected := "9007199254740993"
					call := ToolCall{ID: "fixture-1", Name: "lookup", Arguments: map[string]any{"id": json.Number(expected)}}
					if scenario == "edited" {
						expected = "9007199254740995"
						call.Arguments["id"] = json.Number(expected)
					}
					if scenario == "rounded" {
						call.Arguments["id"] = float64(9007199254740992)
					}
					if scenario == "conflict" {
						call.RawArguments = `{"id":1}`
					}
					if scenario == "restored" {
						data, _ := json.Marshal(call)
						var copy ToolCall
						d := json.NewDecoder(strings.NewReader(string(data)))
						d.UseNumber()
						if err := d.Decode(&copy); err != nil {
							t.Fatal(err)
						}
						call = copy
					}
					conversation, schemas := kiroAuthoredFixture(call)
					conversation.Tools[0].Parameters = map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer"}}}
					config.Schemas = schemas
					var credentials, httpCalls atomic.Int32
					source, executor := config.Credentials, config.RoundTrip
					config.Credentials = func(ctx context.Context, scope KiroCredentialScope) (KiroScopedCredential, error) {
						credentials.Add(1)
						return source(ctx, scope)
					}
					config.RoundTrip = func(r *http.Request) (*http.Response, error) {
						httpCalls.Add(1)
						body, err := io.ReadAll(r.Body)
						if err != nil {
							return nil, err
						}
						var wire struct {
							Conversation struct {
								History []struct {
									Assistant *struct {
										Calls []struct {
											Input map[string]json.Number `json:"input"`
										} `json:"toolUses"`
									} `json:"assistantResponseMessage"`
								} `json:"history"`
							} `json:"conversationState"`
						}
						if err := json.Unmarshal(body, &wire); err != nil {
							return nil, err
						}
						if len(wire.Conversation.History) != 2 || wire.Conversation.History[1].Assistant == nil || len(wire.Conversation.History[1].Assistant.Calls) != 1 || wire.Conversation.History[1].Assistant.Calls[0].Input["id"] != json.Number(expected) {
							return nil, fmt.Errorf("fixture authored request mismatch")
						}
						return executor(r)
					}
					var err error
					if incremental {
						if scenario == "strict-reject" {
							err = RegisterKiroIncrementalModule(t.Name(), KiroIncrementalConfig(config), KiroIncrementalOptions{})
						} else {
							err = RegisterKiroIncrementalModuleWithAuthoredToolHistory(t.Name(), KiroIncrementalConfig(config), KiroIncrementalOptions{})
						}
					} else {
						if scenario == "strict-reject" {
							err = RegisterKiroBufferedModule(t.Name(), config)
						} else {
							err = RegisterKiroBufferedModuleWithOptions(t.Name(), config, WithKiroCallerAuthoredToolHistory())
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					defer UnregisterAPIModules(t.Name())
					if credentials.Load() != 0 || httpCalls.Load() != 0 {
						t.Fatal("registration invoked source/network")
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					if scenario == "cancelled" {
						cancel()
					}
					var stream *AssistantMessageEventStream
					if simple {
						stream = StreamSimple(model, conversation, SimpleStreamOptions{RequestContext: ctx, Transport: TransportSSE})
					} else {
						stream = Stream(model, conversation, ProviderStreamOptions{RequestContext: ctx, Transport: TransportSSE})
					}
					for range stream.Events() {
					}
					result := stream.Result()
					stream.Close()
					success := scenario == "authored" || scenario == "edited" || scenario == "restored"
					if success {
						if result.StopReason != StopReasonStop || credentials.Load() != 1 || httpCalls.Load() != 1 {
							t.Fatalf("authored path failed: %s %s %d %d", result.StopReason, result.ErrorMessage, credentials.Load(), httpCalls.Load())
						}
					} else if result.StopReason != StopReasonError || credentials.Load() != 0 || httpCalls.Load() != 0 {
						t.Fatalf("rejected history reached source: %s %d %d", result.StopReason, credentials.Load(), httpCalls.Load())
					}
					if scenario != "conflict" && conversation.Messages[1].(AssistantMessage).Content[1].(ToolCall).RawArguments != "" {
						t.Fatal("request path forged raw evidence")
					}
				})
			}
		}
	}
}

func TestKiroAuthoredRegistrationOptionsValidateBeforeCallbacks(t *testing.T) {
	config, _ := kiroRegistrationFixture(t)
	if err := RegisterKiroBufferedModuleWithOptions(t.Name(), config, WithKiroCallerAuthoredToolHistory(), WithKiroCallerAuthoredToolHistory()); err == nil {
		UnregisterAPIModules(t.Name())
		t.Fatal("duplicate authored policy accepted")
	}
	if resolveAPIModule(config.API) != nil {
		t.Fatal("invalid policy registered")
	}
	if err := RegisterKiroBufferedModuleWithOptions(t.Name(), config, WithKiroAutoHTTP(), WithKiroCallerAuthoredToolHistory(), WithKiroSessionAffinity(KiroSessionAffinity{Tenant: "fixture", Key: []byte(strings.Repeat("K", 32))})); err != nil {
		t.Fatal(err)
	}
	UnregisterAPIModules(t.Name())
}
