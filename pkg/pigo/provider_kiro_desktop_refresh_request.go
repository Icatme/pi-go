package pigo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
)

var errKiroDesktopRefreshRequest = errors.New("kiro: invalid offline desktop refresh request")

type kiroDesktopRefreshRequestOptions struct {
	Scope        kiroCredentialScope
	UserAgent    string
	RefreshToken string `json:"-"`
	MaxBytes     int
}

func (kiroDesktopRefreshRequestOptions) String() string {
	return "[kiro desktop refresh request options]"
}
func (kiroDesktopRefreshRequestOptions) GoString() string {
	return "[kiro desktop refresh request options]"
}

// newKiroOfflineDesktopRefreshRequest constructs only the community-observed
// desktop request. It does not send, discover credentials, launch desktop/CLI,
// register/login, store tokens or impersonate a default client identity. Scope is
// asserted, not verified, and endpoint existence/authorization are not inferred.
// Returned body contains a secret: no logging, persistence, redirects or retries.
func newKiroOfflineDesktopRefreshRequest(ctx context.Context, options kiroDesktopRefreshRequestOptions) (*http.Request, error) {
	if ctx == nil || ctx.Err() != nil || options.MaxBytes < 1 || options.MaxBytes > 1<<20 || len(options.Scope.Region) > 63 || !kiroRuntimeRegionShape.MatchString(options.Scope.Region) || !kiroCatalogLabel(options.Scope.ProfileARN, 2048) || !kiroExplicitUserAgentValid(options.UserAgent) || !kiroCatalogLabel(options.RefreshToken, 16384) {
		return nil, errKiroDesktopRefreshRequest
	}
	body, err := json.Marshal(struct {
		RefreshToken string `json:"refreshToken"`
	}{options.RefreshToken})
	if err != nil || len(body) > options.MaxBytes || ctx.Err() != nil {
		return nil, errKiroDesktopRefreshRequest
	}
	endpoint := url.URL{Scheme: "https", Host: "prod." + options.Scope.Region + ".auth.desktop.kiro.dev", Path: "/refreshToken"}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil || ctx.Err() != nil {
		return nil, errKiroDesktopRefreshRequest
	}
	request.GetBody = nil
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", options.UserAgent)
	return request, nil
}
