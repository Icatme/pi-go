package pigo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
)

func kiroRegistrationFixture(t *testing.T) (KiroBufferedConfig, Model) {
	t.Helper()
	opts := kiroHTTPRequestFixture()
	api := API(t.Name())
	provider := Provider("explicit-kiro-fixture")
	return KiroBufferedConfig{API: api, Provider: provider, Region: opts.Region, ProfileARN: opts.Identity.ProfileARN, Origin: opts.Identity.Origin, UserAgent: opts.UserAgent,
		RequestIDs: func(context.Context) (KiroRequestIDs, error) {
			return KiroRequestIDs{ConversationID: "test-conversation", InvocationID: opts.InvocationID}, nil
		},
		Credentials: func(_ context.Context, scope KiroCredentialScope) (KiroScopedCredential, error) {
			return KiroScopedCredential{Scope: scope, Kind: KiroCredentialBearer, Token: "LOCAL_ONLY_REGISTRATION"}, nil
		},
		RoundTrip: func(*http.Request) (*http.Response, error) {
			return kiroAttemptResponse(io.NopCloser(bytes.NewReader(kiroOfflineJoin(kiroOfflineEvent("assistantResponseEvent", `{"content":"answer"}`), kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`))))), nil
		},
	}, Model{API: api, Provider: provider, ID: opts.Identity.ModelID}
}

func TestKiroExplicitRegistrationNoCallbacksUntilDispatch(t *testing.T) {
	for _, simple := range []bool{false, true} {
		t.Run(map[bool]string{false: "full", true: "simple"}[simple], func(t *testing.T) {
			config, model := kiroRegistrationFixture(t)
			ids, credentials, httpCalls := 0, 0, 0
			idSource, credentialSource, executor := config.RequestIDs, config.Credentials, config.RoundTrip
			config.RequestIDs = func(ctx context.Context) (KiroRequestIDs, error) { ids++; return idSource(ctx) }
			config.Credentials = func(ctx context.Context, scope KiroCredentialScope) (KiroScopedCredential, error) {
				credentials++
				return credentialSource(ctx, scope)
			}
			config.RoundTrip = func(request *http.Request) (*http.Response, error) {
				httpCalls++
				if request.Header.Get("Authorization") != "Bearer LOCAL_ONLY_REGISTRATION" {
					t.Fatal("auth")
				}
				return executor(request)
			}
			if err := RegisterKiroBufferedModule(t.Name(), config); err != nil {
				t.Fatal(err)
			}
			defer UnregisterAPIModules(t.Name())
			if ids+credentials+httpCalls != 0 {
				t.Fatal("registration executed callback")
			}
			conversation := Context{Messages: []Message{UserMessage{Content: "hello"}}}
			var stream *AssistantMessageEventStream
			if simple {
				stream = StreamSimple(model, conversation, SimpleStreamOptions{RequestContext: context.Background()})
			} else {
				stream = Stream(model, conversation, ProviderStreamOptions{RequestContext: context.Background()})
			}
			for range stream.Events() {
			}
			result := stream.Result()
			stream.Close()
			if result.StopReason != StopReasonStop || ids != 1 || credentials != 1 || httpCalls != 1 {
				t.Fatal("dispatch", result.StopReason, ids, credentials, httpCalls)
			}
			UnregisterAPIModules(t.Name())
			stream = StreamSimple(model, conversation, SimpleStreamOptions{RequestContext: context.Background()})
			for range stream.Events() {
			}
			stream.Close()
			if httpCalls != 1 {
				t.Fatal("dispatch after unregister")
			}
		})
	}
}

func TestKiroExplicitRegistrationRejectsOptionsAndInputBeforeCredentials(t *testing.T) {
	for _, scenario := range []string{"api-key", "custom-client", "nil-context", "cancelled", "dynamic-system", "default-system", "model-url", "identity-error", "credential-error"} {
		t.Run(scenario, func(t *testing.T) {
			config, model := kiroRegistrationFixture(t)
			credentials, httpCalls := 0, 0
			original := config.Credentials
			config.Credentials = func(ctx context.Context, scope KiroCredentialScope) (KiroScopedCredential, error) {
				credentials++
				if scenario == "credential-error" {
					return KiroScopedCredential{}, errors.New("PRIVATE")
				}
				return original(ctx, scope)
			}
			config.RoundTrip = func(*http.Request) (*http.Response, error) { httpCalls++; return nil, nil }
			if scenario == "identity-error" {
				config.RequestIDs = func(context.Context) (KiroRequestIDs, error) { return KiroRequestIDs{}, errors.New("PRIVATE") }
			}
			if err := RegisterKiroBufferedModule(t.Name(), config); err != nil {
				t.Fatal(err)
			}
			defer UnregisterAPIModules(t.Name())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			options := SimpleStreamOptions{RequestContext: ctx}
			conversation := Context{Messages: []Message{UserMessage{Content: "hello"}}}
			switch scenario {
			case "api-key":
				options.APIKey = "PRIVATE"
			case "custom-client":
				options.HTTPClient = &http.Client{}
			case "nil-context":
				options.RequestContext = nil
			case "cancelled":
				cancel()
			case "dynamic-system":
				conversation.Messages = append(conversation.Messages, SystemMessage{Content: "later"})
			case "default-system":
				conversation.SystemPrompt = "must not silently drop"
			case "model-url":
				model.BaseURL = "https://wrong.invalid"
			}
			stream := StreamSimple(model, conversation, options)
			for range stream.Events() {
			}
			result := stream.Result()
			stream.Close()
			expected := 0
			if scenario == "credential-error" {
				expected = 1
			}
			if result.StopReason != StopReasonError && result.StopReason != StopReasonAborted {
				t.Fatal("not failed")
			}
			if credentials != expected || httpCalls != 0 || bytes.Contains([]byte(result.ErrorMessage), []byte("PRIVATE")) {
				t.Fatal("side effect or leak")
			}
		})
	}
}

func TestKiroExplicitRegistrationToolsReplayAndSchemaSnapshot(t *testing.T) {
	config, model := kiroRegistrationFixture(t)
	raw := json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}`)
	parameters := map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer"}}, "required": []string{"id"}}
	config.Schemas = map[string]json.RawMessage{"lookup": raw}
	attempts := 0
	config.RoundTrip = func(request *http.Request) (*http.Response, error) {
		attempts++
		body, _ := io.ReadAll(request.Body)
		if !bytes.Contains(body, []byte(`"lookup"`)) {
			t.Fatal("tool schema missing")
		}
		if attempts == 1 {
			return kiroAttemptResponse(io.NopCloser(bytes.NewReader(kiroInjectedToolWire()))), nil
		}
		if !bytes.Contains(body, []byte("9007199254740993")) || !bytes.Contains(body, []byte(`"toolResults"`)) {
			t.Fatal("raw replay missing")
		}
		return kiroAttemptResponse(io.NopCloser(bytes.NewReader(kiroOfflineJoin(kiroOfflineEvent("assistantResponseEvent", `{"content":"done"}`), kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`))))), nil
	}
	if err := RegisterKiroBufferedModule(t.Name(), config); err != nil {
		t.Fatal(err)
	}
	defer UnregisterAPIModules(t.Name())
	raw[0] = '!'
	delete(config.Schemas, "lookup") // registration owns its detached catalog
	conversation := Context{Messages: []Message{SystemMessage{ToolsAdded: []Tool{{Name: "lookup", Parameters: parameters}}}, UserMessage{Content: "find"}}}
	options := SimpleStreamOptions{RequestContext: context.Background()}
	stream := StreamSimple(model, conversation, options)
	for range stream.Events() {
	}
	result := stream.Result()
	stream.Close()
	if result.StopReason != StopReasonToolUse {
		t.Fatal("tool response", result.ErrorMessage)
	}
	call := result.Content[0].(ToolCall)
	if call.RawArguments != "{\"id\":9007199254740993}" {
		t.Fatal("raw evidence")
	}
	conversation.Messages = append(conversation.Messages, result, ToolResultMessage{ToolCallID: call.ID, ToolName: call.Name, Content: []ContentBlock{TextContent{Text: "fake result, not executed"}}})
	stream = StreamSimple(model, conversation, options)
	for range stream.Events() {
	}
	result = stream.Result()
	stream.Close()
	if result.StopReason != StopReasonStop || attempts != 2 {
		t.Fatal("second turn", result.ErrorMessage)
	}
}

func TestKiroExplicitRegistrationInvalidConfigNeverRegisters(t *testing.T) {
	for _, mutate := range []func(*KiroBufferedConfig){func(c *KiroBufferedConfig) { c.RequestIDs = nil }, func(c *KiroBufferedConfig) { c.Credentials = nil }, func(c *KiroBufferedConfig) { c.RoundTrip = nil }, func(c *KiroBufferedConfig) { c.Region = "invalid" }, func(c *KiroBufferedConfig) { c.Origin = "" }, func(c *KiroBufferedConfig) { c.SystemPromptPolicy = "silent" }, func(c *KiroBufferedConfig) { c.Schemas = map[string]json.RawMessage{"x": []byte(`[]`)} }} {
		config, _ := kiroRegistrationFixture(t)
		mutate(&config)
		if err := RegisterKiroBufferedModule(t.Name(), config); err != errKiroRegistration {
			t.Fatal("invalid config")
		}
		if resolveAPIModule(config.API) != nil {
			t.Fatal("registered invalid config")
		}
	}
}

func TestKiroExplicitRegistrationNoToolEventsOnIncompleteResponse(t *testing.T) {
	for _, scenario := range []string{"truncated", "close", "cancel", "unexpected-tool"} {
		t.Run(scenario, func(t *testing.T) {
			config, model := kiroRegistrationFixture(t)
			raw := json.RawMessage(`{"type":"object"}`)
			config.Schemas = map[string]json.RawMessage{"lookup": raw}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wire := kiroInjectedToolWire()
			if scenario == "truncated" {
				wire = wire[:len(wire)-1]
			}
			body := &kiroAttemptBody{reader: bytes.NewReader(wire)}
			if scenario == "close" {
				body.closeErr = errors.New("PRIVATE_CLOSE")
			}
			config.RoundTrip = func(*http.Request) (*http.Response, error) {
				if scenario == "cancel" {
					cancel()
				}
				return kiroAttemptResponse(body), nil
			}
			if err := RegisterKiroBufferedModule(t.Name(), config); err != nil {
				t.Fatal(err)
			}
			defer UnregisterAPIModules(t.Name())
			conversation := Context{Tools: []Tool{{Name: "lookup", Parameters: map[string]any{"type": "object"}}}, Messages: []Message{UserMessage{Content: "hello"}}}
			if scenario == "unexpected-tool" {
				conversation.Tools = nil
			}
			stream := StreamSimple(model, conversation, SimpleStreamOptions{RequestContext: ctx})
			for event := range stream.Events() {
				if event.Type == AssistantMessageEventDone || event.Type == AssistantMessageEventToolCallStart || event.Type == AssistantMessageEventToolCallDelta || event.Type == AssistantMessageEventToolCallEnd {
					t.Fatal("tool/success event escaped")
				}
			}
			result := stream.Result()
			stream.Close()
			if result.StopReason != StopReasonError && result.StopReason != StopReasonAborted {
				t.Fatal("successful failed attempt")
			}
			if body.closes != 1 {
				t.Fatal("body not closed")
			}
		})
	}
}

func TestKiroPublicEntryValidatesOriginalHistory(t *testing.T) {
	for _, simple := range []bool{false, true} {
		t.Run(map[bool]string{false: "Stream", true: "StreamSimple"}[simple], func(t *testing.T) {
			config, model := kiroRegistrationFixture(t)
			config.SystemPromptPolicy = "prepend_first_user"
			config.Schemas = map[string]json.RawMessage{"lookup": json.RawMessage(`{"type":"object"}`)}
			var wire []byte
			var creds int
			source, transport := config.Credentials, config.RoundTrip
			config.Credentials = func(ctx context.Context, scope KiroCredentialScope) (KiroScopedCredential, error) {
				creds++
				return source(ctx, scope)
			}
			config.RoundTrip = func(req *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					return nil, err
				}
				wire = body
				return transport(req)
			}
			if err := RegisterKiroBufferedModuleWithOptions(t.Name(), config, WithKiroCallerAuthoredToolHistory()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { UnregisterAPIModules(t.Name()) })
			run := func(input Context) AssistantMessage {
				if simple {
					return CompleteSimple(model, input, SimpleStreamOptions{RequestContext: t.Context()})
				}
				return Complete(model, input, ProviderStreamOptions{RequestContext: t.Context()})
			}
			cycle := map[string]any{}
			cycle["self"] = cycle
			call := ToolCall{ID: "one", Name: "lookup", Arguments: cycle}
			tools := []Tool{{Name: "lookup", Parameters: map[string]any{"type": "object"}}}
			history := func(block ContentBlock) Context {
				return Context{Messages: []Message{
					&SystemMessage{Content: "policy"},
					&UserMessage{Content: "question"},
					&AssistantMessage{StopReason: StopReasonStop, Content: []ContentBlock{TextContent{Text: "left"}, block}},
					UserMessage{Content: "continue"},
				}}
			}
			t.Run("pointer text and initial policy", func(t *testing.T) {
				if got := run(history(&TextContent{Text: "right"})); got.StopReason != StopReasonStop || !bytes.Contains(wire, []byte(`"content":"leftright"`)) || !bytes.Contains(wire, []byte(`"content":"policy\n\nquestion"`)) {
					t.Fatalf("pointer history was lost in transit: %q, %s", got.StopReason, wire)
				}
			})
			for _, tt := range []struct {
				name  string
				input Context
			}{
				{"signed pointer history", history(&TextContent{Text: "right", TextSignature: "opaque"})},
				{"nil pointer history", history((*TextContent)(nil))},
				{"nil message", Context{Messages: []Message{UserMessage{Content: "q"}, (*AssistantMessage)(nil), UserMessage{Content: "q"}}}},
				{"constrained sampling", Context{Tools: []Tool{{Name: "lookup", Parameters: map[string]any{"type": "object"}, ConstrainedSampling: &ToolConstrainedSampling{Type: "text"}}}, Messages: []Message{UserMessage{Content: "q"}}}},
				{"cyclic arguments", Context{Tools: tools, Messages: []Message{
					UserMessage{Content: "q"},
					AssistantMessage{StopReason: StopReasonToolUse, Content: []ContentBlock{call}},
					ToolResultMessage{ToolCallID: "one", ToolName: "lookup", Content: []ContentBlock{TextContent{Text: "ok"}}},
				}}},
			} {
				t.Run(tt.name, func(t *testing.T) {
					before := creds
					wire = nil
					if got := run(tt.input); got.StopReason != StopReasonError || creds != before || wire != nil {
						t.Fatalf("invalid input reached credentials or transport: %s", got.StopReason)
					}
				})
			}
		})
	}
}
