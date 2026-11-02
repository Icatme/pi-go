package pigo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// RegisterKiroAgentFixtureForTest exists ONLY in the test build. Never exported
// by the production package. No supplied HTTP client/default transport is used.
func RegisterKiroAgentFixtureForTest(source string, exchange func(context.Context, []byte) (string, error), onDispatch func(Context)) (Provider, func()) {
	provider := Provider("fixture-kiro-agent-" + source)
	api := API("fixture-kiro-agent-" + source)
	model := Model{ID: "fixture-model", Provider: provider, API: api, Name: "Offline fixture"}
	RegisterProviderModule(ProviderModule{Provider: provider, Models: map[string]Model{model.ID: model}, ModelCapabilities: ModelCapabilities{CustomTools: CapabilitySupported}})
	RegisterAPIModuleForSource(source, APIModule{API: api, StreamSimple: func(m Model, ctx Context, options SimpleStreamOptions) *AssistantMessageEventStream {
		if onDispatch != nil {
			onDispatch(ctx)
		}
		// Permit only initial empty system/tool declarations. Do not silently fold
		// a real system instruction or later system changes into a different role.
		for i, message := range ctx.Messages {
			if _, ok := message.(SystemMessage); ok && i != 0 {
				return streamAPIUnavailable(m, "fixture rejects later system changes")
			}
		}
		resolved := resolveProviderContext(m, ctx)
		if resolved.SystemPrompt != "" || len(resolved.HostedTools) != 0 {
			return streamAPIUnavailable(m, "fixture rejects system/hosted semantics")
		}
		schemas := make(map[string]json.RawMessage, len(resolved.Tools))
		for _, tool := range resolved.Tools {
			raw, err := json.Marshal(tool.Parameters)
			if err != nil {
				return streamAPIUnavailable(m, "fixture schema")
			}
			schemas[tool.Name] = raw
		}
		if len(schemas) == 0 {
			schemas = nil
		}
		// Verify the actual agent bridge and host transcript preserved original
		// raw evidence before request serialization is allowed to compact JSON.
		for _, message := range resolved.Messages {
			if assistant, ok := message.(AssistantMessage); ok {
				for _, block := range assistant.Content {
					if call, ok := block.(ToolCall); ok && call.RawArguments != "" && call.RawArguments != kiroAgentFixtureRaw {
						return streamAPIUnavailable(m, "fixture original evidence missing")
					}
				}
			}
		}
		_, _, config := kiroInjectedToolFixture()
		config.Model = m
		stream, err := runKiroInjectedCompletedStream(options.RequestContext, resolved, schemas, config, kiroCredentialBearer, "LOCAL_ONLY_AGENT_FIXTURE", func(request *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				return nil, err
			}
			scenario, err := exchange(request.Context(), body)
			if err != nil {
				return nil, err
			}
			var wire []byte
			switch scenario {
			case "tool", "truncated":
				payload, _ := json.Marshal(map[string]any{"name": "lookup", "toolUseId": "agent-wire-1", "input": kiroAgentFixtureRaw, "stop": true})
				wire = kiroOfflineJoin(kiroOfflineEvent("toolUseEvent", string(payload)), kiroOfflineEvent("metadataEvent", `{"stopReason":"TOOL_USE"}`))
				if scenario == "truncated" {
					wire = wire[:len(wire)-1]
				}
			case "text":
				wire = kiroOfflineJoin(kiroOfflineEvent("assistantResponseEvent", `{"content":"fixture complete"}`), kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`))
			default:
				return nil, errors.New("unsupported fixture scenario")
			}
			return kiroAttemptResponse(io.NopCloser(bytes.NewReader(wire))), nil
		})
		if err != nil {
			return streamAPIUnavailable(m, "fixture attempt rejected")
		}
		return stream
	}})
	return provider, func() { UnregisterAPIModules(source); providerRegistry.Delete(provider) }
}

const kiroAgentFixtureRaw = " { \"id\" : 9007199254740993 } "
