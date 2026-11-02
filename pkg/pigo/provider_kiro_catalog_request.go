package pigo

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

var errKiroCatalogRequest = errors.New("kiro: invalid offline catalog request")

type kiroCatalogRequestOptions struct {
	Scope     kiroCredentialScope
	Origin    string
	UserAgent string
}

// newKiroOfflineCatalogRequest constructs but NEVER sends ListAvailableModels.
// All routing/client identity is explicit. URL/query validation precedes exactly
// one scoped credential callback. No profile discovery, region fallback, retry,
// cache, registry, default client or account entitlement inference. The caller
// must not log the returned request: headers contain a token and URL a profile.
// Source is trusted/context-cooperative; arbitrary callbacks cannot be preempted.
func newKiroOfflineCatalogRequest(ctx context.Context, options kiroCatalogRequestOptions, source kiroCredentialSource) (*http.Request, error) {
	if ctx == nil || ctx.Err() != nil || source == nil || len(options.Scope.Region) > 63 || !kiroRuntimeRegionShape.MatchString(options.Scope.Region) || !kiroCatalogLabel(options.Scope.ProfileARN, 2048) || !kiroCatalogLabel(options.Origin, 64) || !kiroExplicitUserAgentValid(options.UserAgent) {
		return nil, errKiroCatalogRequest
	}
	endpoint := url.URL{Scheme: "https", Host: "management." + options.Scope.Region + ".kiro.dev", Path: "/List-Available-Models"}
	query := url.Values{"origin": []string{options.Origin}, "profileArn": []string{options.Scope.ProfileARN}}
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil || ctx.Err() != nil {
		return nil, errKiroCatalogRequest
	}
	credential, err := source(ctx, options.Scope)
	if err != nil || ctx.Err() != nil || credential.Scope != options.Scope {
		return nil, errKiroCatalogRequest
	}
	headers, err := buildKiroExplicitAuthHeaders(credential.Kind, credential.Token)
	if err != nil || ctx.Err() != nil {
		return nil, errKiroCatalogRequest
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", options.UserAgent)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if ctx.Err() != nil {
		return nil, errKiroCatalogRequest
	}
	return request, nil
}
