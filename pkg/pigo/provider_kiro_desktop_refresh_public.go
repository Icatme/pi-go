package pigo

import (
	"context"
	"net/http"
	"time"
)

// KiroDesktopRefreshConfig requires caller-owned inputs and an explicit executor.
// Use keyed literals. Clock runs before dispatch and after successful Close; no
// default network, desktop/CLI login, source update, store or retry is installed.
// The community-observed endpoint is not an official third-party API guarantee.
type KiroDesktopRefreshConfig struct {
	Scope                             KiroCredentialScope
	UserAgent                         string
	RefreshToken                      string `json:"-"`
	MaxRequestBytes, MaxResponseBytes int
	MaxLifetime, RefreshSkew          time.Duration
	Clock                             func(context.Context) (time.Time, error)
	RoundTrip                         func(*http.Request) (*http.Response, error)
}

func (KiroDesktopRefreshConfig) String() string   { return "[kiro desktop refresh config]" }
func (KiroDesktopRefreshConfig) GoString() string { return "[kiro desktop refresh config]" }

// KiroDesktopRefreshError carries no raw cause or secret. ExecutorInvoked reports
// control handoff only; true may hide server-side rotation and is not retry-safe.
type KiroDesktopRefreshError struct{ executorInvoked bool }

func (*KiroDesktopRefreshError) Error() string           { return "kiro: explicit desktop refresh failed" }
func (e *KiroDesktopRefreshError) ExecutorInvoked() bool { return e != nil && e.executorInvoked }

// KiroDesktopRefreshResult is an opaque in-memory observation, not stored account
// access. Explicit getters disclose copies of secret values; do not log them.
// Scope is asserted; a matching returned profile does not prove ownership.
type KiroDesktopRefreshResult struct{ result kiroRefreshResult }

func (KiroDesktopRefreshResult) String() string                     { return "[kiro desktop refresh result]" }
func (KiroDesktopRefreshResult) GoString() string                   { return "[kiro desktop refresh result]" }
func (r KiroDesktopRefreshResult) Credential() KiroScopedCredential { return r.result.Credential }
func (r KiroDesktopRefreshResult) RefreshToken() string             { return r.result.RefreshToken }
func (r KiroDesktopRefreshResult) ExpiresAt() time.Time             { return r.result.ExpiresAt }
func (r KiroDesktopRefreshResult) RefreshAfter() time.Time          { return r.result.RefreshAfter }

// RetainedRefreshToken is provenance, not a continuing-validity guarantee.
func (r KiroDesktopRefreshResult) RetainedRefreshToken() bool { return r.result.retained }

// RefreshKiroDesktop makes at most one explicit executor invocation. Returned
// profileArn must match the asserted profile; true omission of refreshToken may
// retain the request token only after all success checks. Null/empty/error never
// fall back. Caller owns serialization of credential generations and must not
// redirect/retry/log inside the executor. Read/Close/clock cancellation is
// cooperative; plaintext buffers are not securely zeroized. No persistence.
func RefreshKiroDesktop(ctx context.Context, config KiroDesktopRefreshConfig) (*KiroDesktopRefreshResult, error) {
	options := kiroDesktopRefreshAttemptOptions{Request: kiroDesktopRefreshRequestOptions{Scope: config.Scope, UserAgent: config.UserAgent, RefreshToken: config.RefreshToken, MaxBytes: config.MaxRequestBytes}, MaxResponseBytes: config.MaxResponseBytes, MaxLifetime: config.MaxLifetime, RefreshSkew: config.RefreshSkew, Clock: config.Clock}
	result, invoked, err := runKiroInjectedDesktopRefreshAttempt(ctx, options, config.RoundTrip)
	if err != nil {
		return nil, &KiroDesktopRefreshError{executorInvoked: invoked}
	}
	return &KiroDesktopRefreshResult{result: result}, nil
}
