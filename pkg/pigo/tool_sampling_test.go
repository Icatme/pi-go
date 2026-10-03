package pigo

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func samplingTool(kind, mode string) Tool {
	config := &ToolConstrainedSampling{Type: kind, Mode: mode}
	if kind == "grammar" {
		config.Syntax, config.Definition = "lark", "start: /[a-z]+/"
	}
	return Tool{Name: "code", Parameters: map[string]any{"type": "object", "properties": map[string]any{"code": map[string]any{"type": "string"}}, "required": []string{"code"}, "additionalProperties": false}, ConstrainedSampling: config}
}

func TestToolSamplingExactCapabilityAndPreflight(t *testing.T) {
	model := *GetModel("openai", "gpt-6-astra")
	for _, kind := range []string{"text", "grammar"} {
		tool := samplingTool(kind, "require")
		decision, err := ResolveToolSampling(model, tool)
		if err != nil || decision.Representation != "custom" || decision.InputProperty != "code" {
			t.Fatalf("decision=%+v err=%v", decision, err)
		}
		wire := convertOpenAIResponsesTools(map[string]string{tool.Name: decision.InputProperty}, []Tool{tool})[0]
		if wire["type"] != "custom" || wire["parameters"] != nil || wire["strict"] != nil {
			t.Fatalf("wire=%v", wire)
		}
	}
	models := []Model{*GetModel("openai", "gpt-5.4"), *GetModel("openai-codex", "gpt-6-astra"), {ID: "fixture", Provider: "anthropic", API: "anthropic-messages"}}
	unknown := model
	unknown.ID = "gpt-6-astra-unknown-suffix"
	models = append(models, unknown)
	wrongAPI := model
	wrongAPI.API = "openai-completions"
	models = append(models, wrongAPI)
	for _, other := range models {
		for _, simple := range []bool{false, true} {
			tool := samplingTool("grammar", "prefer")
			decision, err := ResolveToolSampling(other, tool)
			if err != nil || decision.Representation != "function" {
				t.Fatalf("%s/%s decision=%v err=%v", other.Provider, other.ID, decision, err)
			}
			if wire := convertOpenAIResponsesTools(nil, []Tool{tool})[0]; wire["type"] != "function" || wire["parameters"] == nil {
				t.Fatalf("fallback=%v", wire)
			}
			tool.ConstrainedSampling.Mode = "require"
			calls := 0
			client := &http.Client{Transport: samplingRoundTripper(func(*http.Request) (*http.Response, error) { calls++; return nil, nil })}
			ctx := Context{Tools: []Tool{tool}}
			var result AssistantMessage
			if simple {
				result = CompleteSimple(other, ctx, SimpleStreamOptions{HTTPClient: client, APIKey: "fixture"})
			} else {
				result = Complete(other, ctx, ProviderStreamOptions{HTTPClient: client, APIKey: "fixture"})
			}
			if result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, "not supported") || calls != 0 {
				t.Fatalf("result=%+v requests=%d", result, calls)
			}
		}
	}
}

type samplingRoundTripper func(*http.Request) (*http.Response, error)

func (f samplingRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestToolSamplingDoesNotInheritProviderFacts(t *testing.T) {
	provider := Provider("test-sampling-facts")
	RegisterProviderModule(ProviderModule{Provider: provider, ModelCapabilities: ModelCapabilities{CustomTools: CapabilitySupported, GrammarTools: CapabilitySupported}, Models: map[string]Model{
		"unknown":    {API: "openai-responses"},
		"explicit":   {API: "openai-responses", Capabilities: ModelCapabilities{CustomTools: CapabilitySupported, GrammarTools: CapabilitySupported}},
		"wrong-wire": {API: "anthropic-messages", Capabilities: ModelCapabilities{CustomTools: CapabilitySupported, GrammarTools: CapabilitySupported}},
	}})
	for id, want := range map[string]CapabilitySupport{"unknown": CapabilityUnknown, "explicit": CapabilitySupported, "wrong-wire": CapabilityUnsupported} {
		facts, ok := LookupModelCapabilities(provider, id)
		if !ok || facts.Capabilities.CustomTools != want || facts.Capabilities.GrammarTools != want {
			t.Fatalf("%s facts=%+v", id, facts)
		}
	}
}

func TestToolSamplingRejectsMalformedDeclarationsAndBounds(t *testing.T) {
	for _, modify := range []func(*Tool){
		func(tool *Tool) { tool.Name = "invalid name" },
		func(tool *Tool) { tool.ConstrainedSampling.Type = "json_schema" },
		func(tool *Tool) { tool.ConstrainedSampling.Mode = "required" },
		func(tool *Tool) { tool.ConstrainedSampling.Syntax = "pcre" },
		func(tool *Tool) { tool.ConstrainedSampling.Definition = " " },
		func(tool *Tool) { tool.ConstrainedSampling.Definition = strings.Repeat("x", MaxToolGrammarBytes+1) },
		func(tool *Tool) { tool.ConstrainedSampling.Definition = string([]byte{0xff}) },
		func(tool *Tool) { tool.Parameters = nil },
		func(tool *Tool) {
			tool.Parameters = json.RawMessage(`{"type":"object","properties":{"\ud800":{"type":"string"}},"required":["\ud800"]}`)
		},
		func(tool *Tool) {
			tool.Parameters = json.RawMessage(`{"type":"object","properties":{"code":{"type":"number"}},"required":["code"]}`)
		},
		func(tool *Tool) {
			tool.Parameters = json.RawMessage(`{"type":"object","properties":{"code":{"type":"string"},"optional":{"type":"string"}},"required":["code"]}`)
		},
		func(tool *Tool) {
			tool.Parameters = json.RawMessage(`{"type":"object","properties":{"code":{"type":"string"}},"required":[]}`)
		},
	} {
		for _, id := range []string{"gpt-6-astra", "gpt-5.4"} {
			tool := samplingTool("grammar", "prefer")
			modify(&tool)
			if _, err := ResolveToolSampling(*GetModel("openai", id), tool); err == nil {
				t.Fatalf("accepted malformed declaration: %+v", tool)
			}
		}
	}
}

func TestToolSamplingTranscriptCloneAndRedefinition(t *testing.T) {
	tool := samplingTool("grammar", "prefer")
	declaration := ToToolDeclaration(tool)
	declaration.ConstrainedSampling.Definition = "start: /x/"
	if tool.ConstrainedSampling.Definition == declaration.ConstrainedSampling.Definition || DeclarationsEqual(tool, declaration) {
		t.Fatal("sampling is aliased or absent from declaration identity")
	}
	changes := GetToolStateChanges([]Tool{tool}, []Tool{declaration})
	if len(changes.ToolsAdded) != 1 || len(changes.ToolsRemoved) != 1 {
		t.Fatalf("changes=%+v", changes)
	}
}

func TestToolSamplingRejectsBeforeAuthorizationAndPayload(t *testing.T) {
	var authCalls, payloadCalls, httpCalls int
	provider := Provider("test-n2-auth-preflight")
	RegisterProviderModule(ProviderModule{Provider: provider, Models: map[string]Model{"fixture": {API: "openai-responses"}}, Auth: ProviderAuth{
		ResolveAuthorization: func(Provider, AuthConfig, *http.Client, context.Context) (string, error) {
			authCalls++
			return "fixture", nil
		},
	}})
	model := *GetModel(provider, "fixture")
	result := Complete(model, Context{Tools: []Tool{samplingTool("grammar", "require")}}, ProviderStreamOptions{
		OnPayload:  func(payload any, model Model) any { payloadCalls++; return payload },
		HTTPClient: &http.Client{Transport: samplingRoundTripper(func(*http.Request) (*http.Response, error) { httpCalls++; return nil, nil })},
	})
	if result.StopReason != StopReasonError || authCalls != 0 || payloadCalls != 0 || httpCalls != 0 {
		t.Fatalf("result=%+v auth=%d payload=%d http=%d", result, authCalls, payloadCalls, httpCalls)
	}
}

func TestToolSamplingRejectsInvalidReplayBeforeRequest(t *testing.T) {
	model := *GetModel("openai", "gpt-6-astra")
	for _, args := range []map[string]any{{}, {"code": 42}, {"code": "x", "extra": true}, {"code": strings.Repeat("x", MaxCustomToolInputBytes+1)}} {
		requests := 0
		ctx := Context{Tools: []Tool{samplingTool("text", "require")}, Messages: []Message{AssistantMessage{Provider: model.Provider, Model: model.ID, API: model.API, StopReason: StopReasonToolUse, Content: []ContentBlock{ToolCall{ID: "call_a|ctc_a", Name: "code", Arguments: args}}}}}
		result := CompleteSimple(model, ctx, SimpleStreamOptions{APIKey: "fixture", HTTPClient: &http.Client{Transport: samplingRoundTripper(func(*http.Request) (*http.Response, error) { requests++; return nil, nil })}})
		if result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, "replay") || requests != 0 {
			t.Fatalf("result=%+v requests=%d", result, requests)
		}
	}
}

func TestToolSamplingRegexAndContextRoundTrip(t *testing.T) {
	model := *GetModel("openai", "gpt-6-astra")
	tool := samplingTool("grammar", "")
	tool.Name = "code_"
	tool.ConstrainedSampling.Syntax, tool.ConstrainedSampling.Definition = "regex", `[a-z]+`
	decision, err := ResolveToolSampling(model, tool)
	if err != nil {
		t.Fatal(err)
	}
	wire := convertOpenAIResponsesTools(map[string]string{tool.Name: decision.InputProperty}, []Tool{tool})[0]
	format := wire["format"].(map[string]any)
	if format["syntax"] != "regex" || format["definition"] != `[a-z]+` {
		t.Fatalf("wire=%v", wire)
	}
	for _, original := range []Context{{Tools: []Tool{tool}}, {Messages: []Message{*CreateInitialSystemMessage("fixture", []Tool{tool})}}} {
		data, err := SerializeContext(original)
		if err != nil {
			t.Fatal(err)
		}
		restored, err := DeserializeContext(data)
		if err != nil {
			t.Fatal(err)
		}
		tools := currentContextTools(restored)
		if len(tools) != 1 || !DeclarationsEqual(tool, tools[0]) {
			t.Fatalf("lost sampling metadata: %s", data)
		}
	}
}

func TestToolSamplingRequestKeepsResolvedRepresentation(t *testing.T) {
	provider := Provider("test-n2-sampling-snapshot")
	module := normalizeProviderModule(provider, ProviderModule{Provider: provider, Models: map[string]Model{"fixture": {API: "openai-responses", Capabilities: ModelCapabilities{CustomTools: CapabilitySupported, GrammarTools: CapabilitySupported}}}})
	RegisterProviderModule(module)
	model := *GetModel(provider, "fixture")
	tool := samplingTool("grammar", "require")
	ctx := Context{Tools: []Tool{tool}, Messages: []Message{AssistantMessage{Provider: provider, Model: model.ID, API: model.API, StopReason: StopReasonToolUse, Content: []ContentBlock{ToolCall{ID: "call_a|ctc_a", Name: "code", Arguments: map[string]any{"code": "abc"}}}}}}
	plan, err := resolveContextToolSampling(model, ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A catalog refresh affects the next request, not the representation chosen
	// for this request's declaration, history and stream decoder.
	changed := module.Models[model.ID]
	changed.Capabilities = ModelCapabilities{CustomTools: CapabilityUnsupported, GrammarTools: CapabilityUnsupported}
	module.Models = map[string]Model{model.ID: changed}
	if !providerRegistry.Replace(provider, &module) {
		t.Fatal("replace fixture catalog")
	}
	for _, request := range []openAIResponsesRequest{buildOpenAIResponsesRequest(plan, model, ctx, ProviderStreamOptions{}), buildOpenAICodexRequest(plan, model, ctx, ProviderStreamOptions{})} {
		if request.Tools[0]["type"] != "custom" {
			t.Fatalf("declaration changed: %v", request.Tools)
		}
		found := false
		for _, item := range request.Input {
			if item["type"] == "custom_tool_call" && item["input"] == "abc" {
				found = true
			}
		}
		if !found {
			t.Fatalf("history changed: %v", request.Input)
		}
	}
	if _, err := resolveContextToolSampling(model, ctx); err == nil {
		t.Fatal("next request ignored revoked capability")
	}
}
