package pigo

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
)

// RegisterKiroLiveAgentFixtureForTest uses production explicit live registration.
// Terminal is withheld until the external test observes an Agent text delta.
func RegisterKiroLiveAgentFixtureForTest(source, tenant string, observe func([]byte), delta <-chan struct{}, fail bool) (Provider, func(), error) {
	provider := Provider("live-agent-" + source)
	api := API("live-agent-" + source)
	model := Model{ID: "fixture-model", Provider: provider, API: api}
	opts := kiroHTTPRequestFixture()
	var sequence atomic.Uint64
	config := KiroLiveTextConfig{API: api, Provider: provider, Region: opts.Region, ProfileARN: opts.Identity.ProfileARN, Origin: "LOCAL_TEST", UserAgent: "local-test/1",
		RequestIDs: func(context.Context) (KiroRequestIDs, error) {
			return KiroRequestIDs{InvocationID: fmt.Sprintf("%08x-0000-0000-0000-000000000000", sequence.Add(1))}, nil
		},
		Credentials: func(_ context.Context, s KiroCredentialScope) (KiroScopedCredential, error) {
			return KiroScopedCredential{Scope: s, Kind: KiroCredentialBearer, Token: "LOCAL_ONLY_LIVE_AGENT"}, nil
		},
		RoundTrip: func(r *http.Request) (*http.Response, error) {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			observe(raw)
			pr, pw := io.Pipe()
			stop := context.AfterFunc(r.Context(), func() { pr.CloseWithError(context.Canceled); pw.CloseWithError(context.Canceled) })
			body := &kiroAttemptBody{reader: pr, onClose: func() { stop(); pr.Close(); pw.Close() }}
			go func() {
				defer pw.Close()
				if _, err := pw.Write(kiroOfflineEvent("assistantResponseEvent", `{"content":"actual agent delta"}`)); err != nil {
					return
				}
				select {
				case <-delta:
				case <-r.Context().Done():
					return
				}
				wire := kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`)
				if fail {
					wire = wire[:len(wire)-1]
				}
				pw.Write(wire)
			}()
			return kiroAttemptResponse(body), nil
		}}
	affinity := KiroSessionAffinity{Tenant: tenant, Key: bytes.Repeat([]byte("K"), 32)}
	if err := RegisterKiroLiveTextModuleWithOptions(source, config, KiroLiveTextOptions{AutoHTTP: true, SessionAffinity: &affinity}); err != nil {
		return "", nil, err
	}
	RegisterProviderModule(ProviderModule{Provider: provider, Models: map[string]Model{model.ID: model}, ModelCapabilities: ModelCapabilities{CustomTools: CapabilityUnsupported}})
	return provider, func() { UnregisterAPIModules(source); providerRegistry.Delete(provider) }, nil
}
