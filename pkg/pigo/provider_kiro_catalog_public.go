package pigo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// KiroCatalogIDKind requires the caller to distinguish alias from service ID.
type KiroCatalogIDKind = kiroCatalogIDKind

const (
	KiroCatalogAlias     = kiroCatalogAlias
	KiroCatalogServiceID = kiroCatalogServiceID
)

var errKiroPublicCatalog = errors.New("kiro: explicit catalog observation failed")

// KiroCatalogFetchConfig explicitly injects every network/auth/clock boundary.
// Use keyed literals. No zero-value network/default scope/limits/clock exists.
// Callbacks must cooperate with context; RoundTrip must not retry or redirect.
// This is an explicit operation, never invoked by provider registration.
type KiroCatalogFetchConfig struct {
	Scope               KiroCredentialScope
	Origin, UserAgent   string
	MaxBytes, MaxModels int
	Clock               func(context.Context) (time.Time, error)
	Credentials         func(context.Context, KiroCredentialScope) (KiroScopedCredential, error)
	RoundTrip           func(*http.Request) (*http.Response, error)
}

func (KiroCatalogFetchConfig) String() string   { return "[kiro catalog fetch config]" }
func (KiroCatalogFetchConfig) GoString() string { return "[kiro catalog fetch config]" }

// KiroModelCatalog is an immutable caller-scoped observation, NOT entitlement,
// a Model registry, cache or serializable persisted catalog. Its zero value fails
// selection. Models/Select return detached observations; no public mutable fields.
type KiroModelCatalog struct{ snapshot *kiroScopedModelCatalog }

func (KiroModelCatalog) String() string   { return "[kiro model catalog]" }
func (KiroModelCatalog) GoString() string { return "[kiro model catalog]" }

// FetchKiroModelCatalog calls only the explicit credentials/executor/clock once
// each where reached. It returns no observation until status/JSON/length/EOF/Close
// validate. It neither installs models/providers nor discovers profiles, chooses
// a default model, reads tokens, logs data, retries or grants service permission.
func FetchKiroModelCatalog(ctx context.Context, config KiroCatalogFetchConfig) (*KiroModelCatalog, error) {
	options := kiroCatalogAttemptOptions{Request: kiroCatalogRequestOptions{Scope: config.Scope, Origin: config.Origin, UserAgent: config.UserAgent}, MaxBytes: config.MaxBytes, MaxModels: config.MaxModels, Clock: config.Clock}
	snapshot, err := runKiroInjectedCatalogAttempt(ctx, options, config.Credentials, config.RoundTrip)
	if err != nil {
		return nil, errKiroPublicCatalog
	}
	return &KiroModelCatalog{snapshot: snapshot}, nil
}

// Models requires matching asserted scope and an explicit freshness policy.
// At maxAge the observation is stale. No capability/price/default inference.
func (c *KiroModelCatalog) Models(scope KiroCredentialScope, now time.Time, maxAge time.Duration) ([]KiroModelObservation, error) {
	if c == nil || !c.snapshot.freshFor(scope, now, maxAge) {
		return nil, errKiroPublicCatalog
	}
	models := make([]KiroModelObservation, len(c.snapshot.models))
	for i, entry := range c.snapshot.models {
		models[i] = KiroModelObservation{entry: cloneKiroCatalogEntry(entry)}
	}
	return models, nil
}

// Select uses an explicit ID namespace; overlapping alias/service strings must
// never be guessed. The returned ServiceID is exact request-time evidence, not a
// runnable host Model. Scope/time are assertions, not authenticated provenance.
func (c *KiroModelCatalog) Select(scope KiroCredentialScope, kind KiroCatalogIDKind, id string, now time.Time, maxAge time.Duration) (KiroModelObservation, error) {
	if c == nil {
		return KiroModelObservation{}, errKiroPublicCatalog
	}
	entry, err := c.snapshot.selectModel(scope, kind, id, now, maxAge)
	if err != nil {
		return KiroModelObservation{}, errKiroPublicCatalog
	}
	return KiroModelObservation{entry: entry}, nil
}

// KiroModelObservation has immutable private state. Unknown limits remain absent.
// Raw getters explicitly expose detached potentially sensitive metadata: do not
// indiscriminately log or interpret schemas as capabilities/account authorization.
type KiroModelObservation struct{ entry kiroCatalogEntry }

func (KiroModelObservation) String() string        { return "[kiro model observation]" }
func (KiroModelObservation) GoString() string      { return "[kiro model observation]" }
func (m KiroModelObservation) Alias() string       { return m.entry.ID }
func (m KiroModelObservation) ServiceID() string   { return m.entry.ServiceID }
func (m KiroModelObservation) DisplayName() string { return m.entry.DisplayName }
func (m KiroModelObservation) MaxInputTokens() (int64, bool) {
	if m.entry.MaxInputTokens == nil {
		return 0, false
	}
	return *m.entry.MaxInputTokens, true
}
func (m KiroModelObservation) MaxOutputTokens() (int64, bool) {
	if m.entry.MaxOutputTokens == nil {
		return 0, false
	}
	return *m.entry.MaxOutputTokens, true
}
func (m KiroModelObservation) RequestFieldsSchema() json.RawMessage {
	return append(json.RawMessage(nil), m.entry.RequestFieldsSchema...)
}
func (m KiroModelObservation) RawMetadata() json.RawMessage {
	return append(json.RawMessage(nil), m.entry.Metadata...)
}
