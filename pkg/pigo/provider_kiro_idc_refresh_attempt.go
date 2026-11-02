package pigo

import (
	"context"
	"errors"
	"net/http"
	"time"
)

var errKiroIDCRefreshAttempt = errors.New("kiro: injected IDC refresh attempt failed")

type kiroIDCRefreshAttemptOptions struct {
	Request                  kiroIDCRefreshRequestOptions
	Scope                    kiroCredentialScope
	MaxResponseBytes         int
	MaxLifetime, RefreshSkew time.Duration
	Clock                    func(context.Context) (time.Time, error)
}

func (kiroIDCRefreshAttemptOptions) String() string   { return "[kiro IDC refresh attempt options]" }
func (kiroIDCRefreshAttemptOptions) GoString() string { return "[kiro IDC refresh attempt options]" }

// runKiroInjectedIDCRefreshAttempt uses exactly one explicit executor invocation.
// The executor must not retry/redirect/log and must cooperate with context; this
// function cannot enforce arbitrary callback behavior. No default network,
// credential discovery/source mutation, persistence, login or retry is added.
// A successful result contains secrets and asserted scope, not verified identity.
func runKiroInjectedIDCRefreshAttempt(ctx context.Context, options kiroIDCRefreshAttemptOptions, roundTrip func(*http.Request) (*http.Response, error)) (kiroRefreshResult, error) {
	zero := kiroRefreshResult{}
	if roundTrip == nil || options.Clock == nil || options.Scope.Region != options.Request.Region || !kiroCatalogLabel(options.Scope.ProfileARN, 2048) || options.MaxResponseBytes < 2 || options.MaxResponseBytes > 1<<20 || options.MaxLifetime < time.Second || options.MaxLifetime > 30*24*time.Hour || options.MaxLifetime%time.Second != 0 || options.RefreshSkew < 0 || options.RefreshSkew > options.MaxLifetime {
		return zero, errKiroIDCRefreshAttempt
	}
	request, err := newKiroOfflineIDCRefreshRequest(ctx, options.Request)
	if err != nil {
		return zero, errKiroIDCRefreshAttempt
	}
	defer request.Body.Close()
	// Anchor the local expiry conservatively before handing control to transport.
	// Response transfer/Close latency must never extend token validity.
	started, err := options.Clock(ctx)
	if err != nil || ctx.Err() != nil || started.IsZero() || started.Year() < 1 || started.Year() > 9998 {
		return zero, errKiroIDCRefreshAttempt
	}
	response, transportErr := roundTrip(request)
	// This reader is JSON-transport-only despite its original catalog name. It
	// closes response+error bodies and validates MIME, length, budget, EOF and Close.
	payload, err := readKiroCatalogHTTPResponse(ctx, response, transportErr, options.MaxResponseBytes)
	if err != nil || ctx.Err() != nil {
		return zero, errKiroIDCRefreshAttempt
	}
	stamp, err := options.Clock(ctx)
	if err != nil || ctx.Err() != nil || stamp.IsZero() || stamp.Year() < 1 || stamp.Year() > 9998 || stamp.Before(started) {
		return zero, errKiroIDCRefreshAttempt
	}
	result, err := parseKiroOfflineRefreshResponse(payload, options.Scope, kiroRefreshIDC, options.Request.RefreshToken, started, options.MaxLifetime, options.RefreshSkew, options.MaxResponseBytes)
	if err != nil || ctx.Err() != nil || !result.ExpiresAt.After(stamp) {
		return zero, errKiroIDCRefreshAttempt
	}
	return result, nil
}
