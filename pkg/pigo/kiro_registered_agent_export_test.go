package pigo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
)

// RegisterKiroSessionAgentFixtureForTest uses the production public registration
// and option hooks; only wire/executor/catalog fixtures are test-only.
func RegisterKiroSessionAgentFixtureForTest(source, tenant string, exchange func(context.Context, []byte) (string, error)) (Provider, func(), error) {
	provider := Provider("registered-agent-" + source)
	api := API("registered-agent-" + source)
	model := Model{ID: "fixture-model", Provider: provider, API: api, Name: "Local registered fixture"}
	opts := kiroHTTPRequestFixture()
	var sequence atomic.Uint64
	config := KiroBufferedConfig{API: api, Provider: provider, Region: opts.Region, ProfileARN: opts.Identity.ProfileARN, Origin: "LOCAL_TEST", UserAgent: "local-test/1", Schemas: map[string]json.RawMessage{"lookup": []byte(`{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}`)},
		RequestIDs: func(context.Context) (KiroRequestIDs, error) {
			return KiroRequestIDs{InvocationID: fmt.Sprintf("%08x-0000-0000-0000-000000000000", sequence.Add(1))}, nil
		},
		Credentials: func(_ context.Context, scope KiroCredentialScope) (KiroScopedCredential, error) {
			return KiroScopedCredential{Scope: scope, Kind: KiroCredentialBearer, Token: "LOCAL_ONLY_REGISTERED_AGENT"}, nil
		},
		RoundTrip: func(request *http.Request) (*http.Response, error) {
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
				wire = kiroInjectedToolWire()
				if scenario == "truncated" {
					wire = wire[:len(wire)-1]
				}
			case "text":
				wire = kiroOfflineJoin(kiroOfflineEvent("assistantResponseEvent", `{"content":"registered fixture complete"}`), kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`))
			case "context-text":
				wire = kiroOfflineJoin(kiroOfflineEvent("contextUsageEvent", `{"contextUsagePercentage":42.5}`), kiroOfflineEvent("assistantResponseEvent", `{"content":"registered fixture complete"}`), kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`))
			default:
				return nil, fmt.Errorf("unsupported local fixture scenario")
			}
			return kiroAttemptResponse(io.NopCloser(bytes.NewReader(wire))), nil
		},
	}
	if err := RegisterKiroSessionBufferedModule(source, config, KiroSessionAffinity{Tenant: tenant, Key: bytes.Repeat([]byte("K"), 32)}); err != nil {
		return "", nil, err
	}
	RegisterProviderModule(ProviderModule{Provider: provider, Models: map[string]Model{model.ID: model}, ModelCapabilities: ModelCapabilities{CustomTools: CapabilitySupported}})
	return provider, func() { UnregisterAPIModules(source); providerRegistry.Delete(provider) }, nil
}
