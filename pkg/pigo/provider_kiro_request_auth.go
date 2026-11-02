package pigo

import (
	"context"
	"encoding/json"
	"net/http"
)

// newKiroOfflineRequestWithToken is an explicit, unsent request constructor.
// It does not authorize service use or verify token/profile/account binding.
// Returned requests CONTAIN SECRETS in headers: never dump or log them.
// Token input is not stored in configuration, URL, body or global state; Go
// strings may still remain in memory. No discovery, persistence or send occurs.
func newKiroOfflineRequestWithToken(ctx context.Context, conversation Context, schemas map[string]json.RawMessage, options kiroHTTPRequestOptions, kind kiroCredentialKind, token string) (*http.Request, error) {
	auth, err := buildKiroExplicitAuthHeaders(kind, token)
	if err != nil {
		return nil, errKiroExplicitAuth
	}
	request, err := newKiroOfflineHTTPRequest(ctx, conversation, schemas, options)
	if err != nil {
		return nil, errKiroHTTPRequest
	}
	for key, value := range auth {
		request.Header.Set(key, value)
	}
	if ctx.Err() != nil {
		return nil, errKiroHTTPRequest
	}
	return request, nil
}
