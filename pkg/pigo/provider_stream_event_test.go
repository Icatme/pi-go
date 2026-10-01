package pigo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
)

func TestProviderStreamEventOptionConversions(t *testing.T) {
	callbackCalls := 0
	callback := func(json.RawMessage, Model) error { callbackCalls++; return nil }
	model := Model{ID: "fixture", MaxTokens: 1024}
	options := []ProviderStreamOptions{
		streamOptionsFromProvider(model, ProviderStreamOptions{OnProviderStreamEvent: callback}).providerStreamOptions(model),
		streamOptionsFromSimple(model, SimpleStreamOptions{OnProviderStreamEvent: callback}).providerStreamOptions(model),
		NewStreamOptions(func(options *StreamOptions) { options.OnProviderStreamEvent = callback }).providerStreamOptions(model),
		(StreamOptions{Common: CommonProviderOptions{OnProviderStreamEvent: callback}}).providerStreamOptions(model),
	}
	for _, provider := range []Provider{"openai", "openai-codex", "anthropic", "google", "mistral", "deepseek", "kimi-coding", "commandcode", "opencode-go"} {
		model.Provider = provider
		options = append(options,
			BuildProviderStreamOptions(model, SimpleStreamOptions{OnProviderStreamEvent: callback}),
			NormalizeProviderStreamOptions(model, ProviderStreamOptions{OnProviderStreamEvent: callback}),
		)
	}
	for i, option := range options {
		if option.OnProviderStreamEvent == nil {
			t.Fatalf("conversion %d dropped callback", i)
		}
		if err := observeProviderStreamEvent(`{"type":"unknown"}`, model, option); err != nil {
			t.Fatal(err)
		}
	}
	if callbackCalls != len(options) {
		t.Fatalf("got %d callbacks, want %d", callbackCalls, len(options))
	}

	// A flat callback must override a stale Common snapshot.
	stale := func(json.RawMessage, Model) error { return errors.New("stale") }
	converted := (StreamOptions{OnProviderStreamEvent: callback, Common: CommonProviderOptions{OnProviderStreamEvent: stale}}).providerStreamOptions(model)
	if err := observeProviderStreamEvent(`{}`, model, converted); err != nil {
		t.Fatalf("flat callback was overridden: %v", err)
	}
}

func TestProviderStreamEventInvalidFramingAndErrorIdentity(t *testing.T) {
	calls := 0
	cause := errors.New("rate limit in caller")
	options := ProviderStreamOptions{OnProviderStreamEvent: func(json.RawMessage, Model) error { calls++; return cause }}
	for _, data := range []string{"", "  ", "[DONE]", "not-json", `{"unfinished":`} {
		if err := observeProviderStreamEvent(data, Model{}, options); err != nil {
			t.Fatalf("framing %q triggered callback: %v", data, err)
		}
	}
	err := observeProviderStreamEvent(`{"unknown":true}`, Model{}, options)
	if calls != 1 || !errors.Is(err, cause) || !isProviderStreamEventCallbackError(fmt.Errorf("wrapped: %w", err)) {
		t.Fatalf("callback cause/type lost: calls=%d err=%v", calls, err)
	}
}

type providerStreamEventFixture struct {
	name     string
	provider Provider
	api      API
	modelID  string
	events   []string
	generate bool
	hosted   bool
	fallback bool
}

type providerStreamEventObserver struct {
	recordingObserver
	textDeltas atomic.Int32
}

func (observer *providerStreamEventObserver) OnStreamEvent(ctx context.Context, model Model, event AssistantMessageEvent) {
	if event.Type == AssistantMessageEventTextDelta {
		observer.textDeltas.Add(1)
	}
	observer.recordingObserver.OnStreamEvent(ctx, model, event)
}

func providerStreamEventFixtures() []providerStreamEventFixture {
	responses := []string{
		`{"type":"response.output_item.done","item":{"type":"message","id":"msg_1","content":[{"type":"output_text","text":"ok"}]}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`,
	}
	chat := []string{`{"id":"chat_1","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`}
	anthropic := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		`{"type":"message_stop"}`,
	}
	return []providerStreamEventFixture{
		{name: "Responses", provider: "openai", api: "openai-responses", modelID: "gpt-fixture", events: responses},
		{name: "Codex SSE", provider: "openai-codex", api: "openai-codex-responses", modelID: "gpt-fixture", events: responses},
		{name: "Completions", provider: "openai", api: "openai-completions", modelID: "gpt-fixture", events: chat},
		{name: "Anthropic", provider: "anthropic", api: "anthropic-messages", modelID: "claude-fixture", events: anthropic},
		{name: "Kimi", provider: "kimi-coding", api: "anthropic-messages", modelID: "kimi-fixture", events: anthropic},
		{name: "Kimi hosted", provider: "kimi-coding", api: "anthropic-messages", modelID: "kimi-fixture", events: anthropic, hosted: true},
		{name: "Google", provider: "google", api: "google-generative-ai", modelID: "gemini-fixture", events: []string{`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`}},
		{name: "Mistral", provider: "mistral", api: "mistral-conversations", modelID: "mistral-fixture", events: chat},
		{name: "DeepSeek", provider: "deepseek", api: "deepseek-chat-completions", modelID: "deepseek-fixture", events: chat},
		{name: "CommandCode native OpenAI", provider: "commandcode", api: "commandcode-custom", modelID: "gpt-5.6-terra", events: chat},
		{name: "CommandCode native Anthropic", provider: "commandcode", api: "commandcode-custom", modelID: "claude-sonnet-5", events: anthropic},
		{name: "CommandCode generate", provider: "commandcode", api: "commandcode-custom", modelID: "gpt-5.6-terra", events: []string{`{"type":"text-delta","text":"ok"}`, `{"type":"finish","finishReason":"stop"}`}, generate: true},
		{name: "CommandCode routed generate", provider: "commandcode", api: "commandcode-custom", modelID: "gpt-5.6-terra", events: []string{`{"type":"text-delta","text":"ok"}`, `{"type":"finish","finishReason":"stop"}`}, fallback: true},
	}
}

func TestProviderStreamEventSSEBeforeNormalizationAndIsolation(t *testing.T) {
	for _, fixture := range providerStreamEventFixtures() {
		for _, simple := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/simple=%t", fixture.name, simple), func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if fixture.fallback && r.URL.Path != "/alpha/generate" {
						w.WriteHeader(http.StatusForbidden)
						fmt.Fprint(w, `{"error":{"code":"upgrade_required"}}`)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, strings.TrimSuffix(providerStreamEventFixtureSSE(fixture.events), "data: [DONE]\n\n"))
				}))
				defer server.Close()
				model := Model{ID: fixture.modelID, Provider: fixture.provider, API: fixture.api, BaseURL: server.URL, MaxTokens: 1024, Input: []InputType{InputText}, Headers: map[string]string{"x-model": "original"}}
				observer := &providerStreamEventObserver{}
				var retained []json.RawMessage
				textEventSeen := false
				callback := func(data json.RawMessage, eventModel Model) error {
					if eventModel.ID != model.ID || eventModel.Provider != model.Provider || eventModel.API != model.API || eventModel.BaseURL != model.BaseURL {
						t.Errorf("public model lost: got %+v want %+v", eventModel, model)
					}
					if strings.Contains(string(data), `"ok"`) && !textEventSeen {
						textEventSeen = true
						if observer.textDeltas.Load() != 0 {
							t.Error("callback ran after normalization")
						}
					}
					retained = append(retained, append(json.RawMessage(nil), data...))
					data[0] = '['
					eventModel.Headers["x-model"] = "changed"
					return nil
				}
				options := ProviderStreamOptions{APIKey: "fixture-key", HTTPClient: server.Client(), Transport: TransportSSE, Observer: observer, OnProviderStreamEvent: callback}
				if fixture.provider == "openai-codex" {
					options.APIKey = makeOpenAICodexToken("fixture-account")
				}
				var stream *AssistantMessageEventStream
				ctx := Context{}
				if fixture.hosted {
					ctx.HostedTools = []HostedTool{{Type: HostedToolTypeWebSearch}}
				}
				if simple {
					simpleOptions := SimpleStreamOptions{APIKey: options.APIKey, HTTPClient: options.HTTPClient, Transport: options.Transport, Observer: observer, OnProviderStreamEvent: callback}
					if fixture.generate {
						stream = streamCommandCodeGenerate(model, ctx, BuildProviderStreamOptions(model, simpleOptions))
					} else {
						stream = StreamSimple(model, ctx, simpleOptions)
					}
				} else if fixture.generate {
					stream = streamCommandCodeGenerate(model, ctx, options)
				} else {
					stream = Stream(model, ctx, options)
				}
				result := stream.Result()
				if result.StopReason != StopReasonStop || len(result.Content) != 1 || result.Content[0].(TextContent).Text != "ok" {
					t.Fatalf("callback affected normalization: %+v", result)
				}
				if len(retained) != len(fixture.events)+1 || !textEventSeen {
					t.Fatalf("missing parsed events: count=%d want=%d textSeen=%t", len(retained), len(fixture.events)+1, textEventSeen)
				}
				wantRequests := int32(1)
				if fixture.fallback {
					wantRequests = 2
				}
				if string(retained[0]) != providerStreamEventUnknownJSON || model.Headers["x-model"] != "original" || requests.Load() != wantRequests {
					t.Fatalf("unknown data/isolation/request count lost: raw=%s model=%+v requests=%d", retained[0], model, requests.Load())
				}
			})
		}
	}
}

const providerStreamEventUnknownJSON = `{"type":"provider.extra","provider_field":{"deep":"value","large":9007199254740993}}`

func providerStreamEventFixtureSSE(events []string) string {
	data := "data: " + providerStreamEventUnknownJSON + "\n\n"
	for _, event := range events {
		data += "data: " + event + "\n\n"
	}
	return data + "data: [DONE]\n\n"
}

func TestProviderStreamEventCallbackFailureStopsWithoutRetry(t *testing.T) {
	for _, fixture := range providerStreamEventFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if fixture.fallback && r.URL.Path != "/alpha/generate" {
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(w, `{"error":{"code":"upgrade_required"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, providerStreamEventFixtureSSE(fixture.events))
			}))
			defer server.Close()
			model := Model{ID: fixture.modelID, Provider: fixture.provider, API: fixture.api, BaseURL: server.URL, MaxTokens: 1024}
			calls := 0
			options := ProviderStreamOptions{APIKey: "fixture-key", HTTPClient: server.Client(), Transport: TransportSSE, MaxRetries: 2, MaxRetryDelay: 1, OnProviderStreamEvent: func(json.RawMessage, Model) error {
				calls++
				return errors.New("intentional rate limit callback failure")
			}}
			if fixture.provider == "openai-codex" {
				options.APIKey = makeOpenAICodexToken("fixture-account")
			}
			var result AssistantMessage
			ctx := Context{}
			if fixture.hosted {
				ctx.HostedTools = []HostedTool{{Type: HostedToolTypeWebSearch}}
			}
			if fixture.generate {
				result = streamCommandCodeGenerate(model, ctx, options).Result()
			} else {
				result = Complete(model, ctx, options)
			}
			wantRequests := int32(1)
			if fixture.fallback {
				wantRequests = 2
			}
			if result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, "intentional rate limit callback failure") || len(result.Content) != 0 || requests.Load() != wantRequests || calls != 1 {
				t.Fatalf("callback failure retried or lost: requests=%d calls=%d result=%+v", requests.Load(), calls, result)
			}
		})
	}
}

func TestProviderStreamEventCodexWebSocket(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("callbackFailure=%t", fail), func(t *testing.T) {
			var requests atomic.Int32
			upgrader := websocket.Upgrader{}
			fixture := providerStreamEventFixtures()[1]
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				if _, _, err := conn.ReadMessage(); err != nil {
					t.Error(err)
					return
				}
				for _, event := range append([]string{providerStreamEventUnknownJSON}, fixture.events...) {
					if err := conn.WriteMessage(websocket.TextMessage, []byte(event)); err != nil {
						return
					}
				}
			}))
			defer server.Close()
			model := Model{ID: "gpt-fixture", Provider: "openai-codex", API: "openai-codex-responses", BaseURL: server.URL, MaxTokens: 1024}
			calls := 0
			result := CompleteSimple(model, Context{}, SimpleStreamOptions{APIKey: makeOpenAICodexToken("fixture-account"), Transport: TransportAuto, MaxRetries: 2, MaxRetryDelay: 1, OnProviderStreamEvent: func(data json.RawMessage, eventModel Model) error {
				calls++
				if eventModel.ID != model.ID || (calls == 1 && string(data) != providerStreamEventUnknownJSON) {
					t.Errorf("provider event lost: model=%+v data=%s", eventModel, data)
				}
				if fail {
					return errors.New("intentional rate limit callback failure")
				}
				data[0] = '['
				return nil
			}})
			if fail {
				if calls != 1 || result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, "intentional rate limit callback failure") {
					t.Fatalf("callback failure lost: calls=%d result=%+v", calls, result)
				}
			} else if calls != len(fixture.events)+1 || result.StopReason != StopReasonStop || len(result.Content) != 1 || result.Content[0].(TextContent).Text != "ok" {
				t.Fatalf("callback affected normalization: calls=%d result=%+v", calls, result)
			}
			if requests.Load() != 1 {
				t.Fatalf("callback retried or fell back to SSE: requests=%d", requests.Load())
			}
		})
	}
}
