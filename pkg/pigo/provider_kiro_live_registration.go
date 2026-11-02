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
	return RegisterKiroLiveTextModuleWithOptions(sourceID, config, KiroLiveTextOptions{})
}

// KiroLiveTextOptions explicitly selects HTTP for Auto and/or session affinity.
// Nil affinity keeps SessionID rejected. No fallback, retry or default client.
// Affinity key material is validated and copied during registration.
type KiroLiveTextOptions struct {
	AutoHTTP        bool
	SessionAffinity *KiroSessionAffinity
}

// RegisterKiroLiveTextModuleWithOptions preserves the strict original entry point.
// Options select behavior before any request; they never retry a partial stream.
func RegisterKiroLiveTextModuleWithOptions(sourceID string, config KiroLiveTextConfig, options KiroLiveTextOptions) error {
	var affinity *KiroSessionAffinity
	if options.SessionAffinity != nil {
		input := options.SessionAffinity
		if !kiroSessionIDValid(input.Tenant) || len(input.Key) < 32 || len(input.Key) > 64 {
			return errKiroLiveRegistration
		}
		copy := *input
		copy.Key = append([]byte(nil), input.Key...)
		affinity = &copy
	}
	shared := KiroBufferedConfig{API: config.API, Provider: config.Provider, Region: config.Region, ProfileARN: config.ProfileARN, Origin: config.Origin, UserAgent: config.UserAgent, SystemPromptPolicy: config.SystemPromptPolicy, RequestIDs: config.RequestIDs, Credentials: config.Credentials, RoundTrip: config.RoundTrip}
	if err := registerKiroExplicitModule(sourceID, shared, affinity, options.AutoHTTP, true); err != nil {
		return errKiroLiveRegistration
	}
	return nil
}
