package pigo

import (
	"context"
	"errors"
	"net/http"
	"time"
)

var errKiroDesktopRefreshAttempt = errors.New("kiro: injected desktop refresh attempt failed")

type kiroDesktopRefreshAttemptOptions struct {
	Request                  kiroDesktopRefreshRequestOptions
	MaxResponseBytes         int
	MaxLifetime, RefreshSkew time.Duration
	Clock                    func(context.Context) (time.Time, error)
}

func (kiroDesktopRefreshAttemptOptions) String() string {
	return "[kiro desktop refresh attempt options]"
}
func (kiroDesktopRefreshAttemptOptions) GoString() string {
	return "[kiro desktop refresh attempt options]"
}

// The bool reports executor invocation, never wire-send/receipt/retry safety.
// Secrets/results remain caller-owned in memory. No default executor, retry,
// redirect, source update, persistence or desktop/CLI discovery is provided.
// Executor/reader/Close/clock are trusted and must cooperate with cancellation.
func runKiroInjectedDesktopRefreshAttempt(ctx context.Context, options kiroDesktopRefreshAttemptOptions, roundTrip func(*http.Request) (*http.Response, error)) (kiroRefreshResult, bool, error) {
	zero := kiroRefreshResult{}
	if roundTrip == nil || options.Clock == nil || options.MaxResponseBytes < 2 || options.MaxResponseBytes > 1<<20 || options.MaxLifetime < time.Second || options.MaxLifetime > 30*24*time.Hour || options.MaxLifetime%time.Second != 0 || options.RefreshSkew < 0 || options.RefreshSkew > options.MaxLifetime {
		return zero, false, errKiroDesktopRefreshAttempt
	}
	request, err := newKiroOfflineDesktopRefreshRequest(ctx, options.Request)
	if err != nil {
		return zero, false, errKiroDesktopRefreshAttempt
	}
	defer request.Body.Close()
	started, err := options.Clock(ctx)
	if err != nil || ctx.Err() != nil || started.IsZero() || started.Year() < 1 || started.Year() > 9998 {
		return zero, false, errKiroDesktopRefreshAttempt
	}
	response, transportErr := roundTrip(request)
	payload, err := readKiroCatalogHTTPResponse(ctx, response, transportErr, options.MaxResponseBytes)
	if err != nil || ctx.Err() != nil {
		return zero, true, errKiroDesktopRefreshAttempt
	}
	ended, err := options.Clock(ctx)
	if err != nil || ctx.Err() != nil || ended.IsZero() || ended.Year() < 1 || ended.Year() > 9998 || ended.Before(started) {
		return zero, true, errKiroDesktopRefreshAttempt
	}
	// The existing desktop parser retains exact old token only on genuine absence,
	// rejects null/empty, and requires returned profileArn to match asserted scope.
	result, err := parseKiroOfflineRefreshResponse(payload, options.Request.Scope, kiroRefreshDesktop, options.Request.RefreshToken, started, options.MaxLifetime, options.RefreshSkew, options.MaxResponseBytes)
	if err != nil || ctx.Err() != nil || !result.ExpiresAt.After(ended) {
		return zero, true, errKiroDesktopRefreshAttempt
	}
	return result, true, nil
}
