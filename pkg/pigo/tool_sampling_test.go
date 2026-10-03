package pigo

import (
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
		wire := convertOpenAIResponsesTools(model, []Tool{tool})[0]
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
			if wire := convertOpenAIResponsesTools(other, []Tool{tool})[0]; wire["type"] != "function" || wire["parameters"] == nil {
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
		func(tool *Tool) { tool.ConstrainedSampling.Type = "json_schema" },
		func(tool *Tool) { tool.ConstrainedSampling.Mode = "required" },
		func(tool *Tool) { tool.ConstrainedSampling.Syntax = "pcre" },
		func(tool *Tool) { tool.ConstrainedSampling.Definition = " " },
		func(tool *Tool) { tool.ConstrainedSampling.Definition = strings.Repeat("x", MaxToolGrammarBytes+1) },
		func(tool *Tool) { tool.ConstrainedSampling.Definition = string([]byte{0xff}) },
		func(tool *Tool) { tool.Parameters = nil },
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
