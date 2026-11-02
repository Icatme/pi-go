package pigo

import (
	"context"
	"errors"
	"net/http"
)

// KiroLiveTextConfig explicitly selects provisional live text, not buffered
// delivery. It intentionally has no tool catalog, session or Auto option. Trusted
// callbacks must cooperate with context. No default network client is installed.
// SystemPromptPolicy has the same explicit downgrade semantics as buffered mode.
type KiroLiveTextConfig struct {
	API                                   API
	Provider                              Provider
	Region, ProfileARN, Origin, UserAgent string
	SystemPromptPolicy                    string
	RequestIDs                            func(context.Context) (KiroRequestIDs, error)
	Credentials                           func(context.Context, KiroCredentialScope) (KiroScopedCredential, error)
	RoundTrip                             func(*http.Request) (*http.Response, error)
}

var errKiroLiveRegistration = errors.New("kiro: invalid explicit live text configuration")

// RegisterKiroLiveTextModule opts into actual incremental text delivery. A late
// failure invalidates completion even if previews were already observed. Tools
// and unsupported host options fail closed; there is no buffered fallback or
// retry. This installs only an API module, not a model/provider or login flow.
// Use Stream/StreamSimple, not raw GetAPIModule callbacks, to retain host guards.
// sourceID owns UnregisterAPIModules cleanup; duplicate API registration panics.
func RegisterKiroLiveTextModule(sourceID string, config KiroLiveTextConfig) error {
	shared := KiroBufferedConfig{API: config.API, Provider: config.Provider, Region: config.Region, ProfileARN: config.ProfileARN, Origin: config.Origin, UserAgent: config.UserAgent, SystemPromptPolicy: config.SystemPromptPolicy, RequestIDs: config.RequestIDs, Credentials: config.Credentials, RoundTrip: config.RoundTrip}
	if err := registerKiroExplicitModule(sourceID, shared, nil, false, true); err != nil {
		return errKiroLiveRegistration
	}
	return nil
}
