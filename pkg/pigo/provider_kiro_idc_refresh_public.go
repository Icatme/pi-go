package pigo

import (
	"context"
	"net/http"
	"time"
)

// KiroIDCRefreshConfig is explicitly caller-owned; use keyed literals. No zero-
// value client, scope, clock, credential, budget, retry or persistence exists.
// The executor MUST NOT retry, redirect or log secrets and must honor context.
// Client credentials must already be authorized; this does not register/login.
type KiroIDCRefreshConfig struct {
	Scope                             KiroCredentialScope
	UserAgent                         string
	ClientID                          string `json:"-"`
	ClientSecret                      string `json:"-"`
	RefreshToken                      string `json:"-"`
	MaxRequestBytes, MaxResponseBytes int
	MaxLifetime, RefreshSkew          time.Duration
	Clock                             func(context.Context) (time.Time, error)
	RoundTrip                         func(*http.Request) (*http.Response, error)
}

func (KiroIDCRefreshConfig) String() string   { return "[kiro IDC refresh config]" }
func (KiroIDCRefreshConfig) GoString() string { return "[kiro IDC refresh config]" }

// KiroIDCRefreshError exposes only whether this helper invoked the executor.
// True is NOT proof bytes were sent, nor that they were not: the server may have
// rotated tokens even if the response/read/Close/parser subsequently failed.
// Never treat this flag as automatic retry permission. No raw cause is retained.
type KiroIDCRefreshError struct{ executorInvoked bool }

func (*KiroIDCRefreshError) Error() string           { return "kiro: explicit IDC refresh failed" }
func (e *KiroIDCRefreshError) ExecutorInvoked() bool { return e != nil && e.executorInvoked }

// KiroIDCRefreshResult holds an immutable in-memory observation, not persisted
// credentials or authenticated account ownership. JSON/fmt do not expose secrets.
// Explicit getters return secrets by value: callers must not log or persist them
// accidentally. This package neither updates nor caches a credential source.
type KiroIDCRefreshResult struct{ result kiroRefreshResult }

func (KiroIDCRefreshResult) String() string                     { return "[kiro IDC refresh result]" }
func (KiroIDCRefreshResult) GoString() string                   { return "[kiro IDC refresh result]" }
func (r KiroIDCRefreshResult) Credential() KiroScopedCredential { return r.result.Credential }
func (r KiroIDCRefreshResult) RefreshToken() string             { return r.result.RefreshToken }
func (r KiroIDCRefreshResult) ExpiresAt() time.Time             { return r.result.ExpiresAt }
func (r KiroIDCRefreshResult) RefreshAfter() time.Time          { return r.result.RefreshAfter }

// RefreshKiroIDC performs one explicitly injected attempt; no default network or
// implicit retry exists. Its response is released only after full HTTP/Close and
// strict parsing checks. A failure after executor invocation has an indeterminate
// server-side rotation outcome. Required returned rotation is a conservative
// subset: an otherwise valid response omitting refreshToken is rejected. Scope
// and clock are caller assertions. Real service compatibility is not guaranteed.
func RefreshKiroIDC(ctx context.Context, config KiroIDCRefreshConfig) (*KiroIDCRefreshResult, error) {
	options := kiroIDCRefreshAttemptOptions{Request: kiroIDCRefreshRequestOptions{Region: config.Scope.Region, UserAgent: config.UserAgent, ClientID: config.ClientID, ClientSecret: config.ClientSecret, RefreshToken: config.RefreshToken, MaxBytes: config.MaxRequestBytes}, Scope: config.Scope, MaxResponseBytes: config.MaxResponseBytes, MaxLifetime: config.MaxLifetime, RefreshSkew: config.RefreshSkew, Clock: config.Clock}
	invoked := false
	var executor func(*http.Request) (*http.Response, error)
	if config.RoundTrip != nil {
		executor = func(r *http.Request) (*http.Response, error) { invoked = true; return config.RoundTrip(r) }
	}
	result, err := runKiroInjectedIDCRefreshAttempt(ctx, options, executor)
	if err != nil {
		return nil, &KiroIDCRefreshError{executorInvoked: invoked}
	}
	return &KiroIDCRefreshResult{result: result}, nil
}
