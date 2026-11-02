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

// RegisterKiroIncrementalAgentFixtureForTest installs the production public mixed
// API. The only executor is an in-memory pipe; no real tools or service exist.
func RegisterKiroIncrementalAgentFixtureForTest(source string, observe func([]byte), preview <-chan struct{}, fault string) (Provider, func(), error) {
	provider := Provider("incremental-agent-" + source)
	api := API("incremental-agent-" + source)
	model := Model{API: api, Provider: provider, ID: "fixture-model"}
	opts := kiroHTTPRequestFixture()
	var sequence, attempt atomic.Uint64
	config := KiroIncrementalConfig{API: api, Provider: provider, Region: opts.Region, ProfileARN: opts.Identity.ProfileARN, Origin: "LOCAL_TEST", UserAgent: "local-test/1", Schemas: map[string]json.RawMessage{"lookup": []byte(`{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}`)},
		RequestIDs: func(context.Context) (KiroRequestIDs, error) {
			return KiroRequestIDs{InvocationID: fmt.Sprintf("%08x-0000-0000-0000-000000000000", sequence.Add(1))}, nil
		}, Credentials: kiroMixedSource,
		RoundTrip: func(r *http.Request) (*http.Response, error) {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			observe(raw)
			if attempt.Add(1) > 1 {
				return kiroAttemptResponse(io.NopCloser(bytes.NewReader(kiroOfflineJoin(kiroOfflineEvent("assistantResponseEvent", `{"content":"finished"}`), kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`))))), nil
			}
			pr, pw := io.Pipe()
			stop := context.AfterFunc(r.Context(), func() { pr.CloseWithError(context.Canceled); pw.CloseWithError(context.Canceled) })
			body := &kiroAttemptBody{reader: pr, onClose: func() { stop(); pr.Close(); pw.Close() }}
			if fault == "close" {
				body.closeErr = fmt.Errorf("LOCAL_PRIVATE_CLOSE")
			}
			go func() {
				defer pw.Close()
				if _, err := pw.Write(kiroOfflineEvent("assistantResponseEvent", `{"content":"looking up"}`)); err != nil {
					return
				}
				select {
				case <-preview:
				case <-r.Context().Done():
					return
				}
				wire := kiroInjectedToolWire()
				if fault == "truncated" {
					wire = wire[:len(wire)-1]
				}
				pw.Write(wire)
			}()
			return kiroAttemptResponse(body), nil
		}}
	affinity := KiroSessionAffinity{Tenant: "local-tenant", Key: bytes.Repeat([]byte("K"), 32)}
	if err := RegisterKiroIncrementalModuleWithAuthoredToolHistory(source, config, KiroIncrementalOptions{AutoHTTP: true, SessionAffinity: &affinity}); err != nil {
		return "", nil, err
	}
	RegisterProviderModule(ProviderModule{Provider: provider, Models: map[string]Model{model.ID: model}, ModelCapabilities: ModelCapabilities{CustomTools: CapabilitySupported}})
	return provider, func() { UnregisterAPIModules(source); providerRegistry.Delete(provider) }, nil
}
