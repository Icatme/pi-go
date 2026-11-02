package pigo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
)

var errKiroHTTPRequest = errors.New("kiro: invalid offline HTTP request")
var kiroRuntimeRegionShape = regexp.MustCompile(`^[a-z]{2}(?:-[a-z]+)+-[0-9]+$`)

type kiroHTTPRequestOptions struct {
	ToolHistoryPolicy kiroToolHistoryPolicy
	// Default rejects system instructions; lowering requires explicit opt-in.
	SystemPromptPolicy kiroSystemPromptPolicy
	Region             string
	Identity           kiroRequestIdentity
	Limits             kiroRequestLimits
	UserAgent          string
	InvocationID       string
}

// newKiroOfflineHTTPRequest assembles but NEVER sends a request. Runtime host
// is derived solely from the explicit region, not an arbitrary URL. Region
// syntax does not prove endpoint availability, profile-region consistency or
// account entitlement. Authentication and transport remain intentionally absent.
func newKiroOfflineHTTPRequest(ctx context.Context, conversation Context, schemas map[string]json.RawMessage, options kiroHTTPRequestOptions) (*http.Request, error) {
	if ctx == nil || ctx.Err() != nil || len(options.Region) > 63 || !kiroRuntimeRegionShape.MatchString(options.Region) {
		return nil, errKiroHTTPRequest
	}
	headers, err := buildKiroOfflineClientHeaders(options.UserAgent, options.InvocationID)
	if err != nil {
		return nil, errKiroHTTPRequest
	}
	prepared, _, err := prepareKiroSystemPrompt(conversation, options.SystemPromptPolicy, options.Limits.PayloadBytes)
	if err != nil {
		return nil, errKiroHTTPRequest
	}
	body, err := encodeKiroOfflineRequestWithPolicy(prepared, schemas, options.Identity, options.Limits, options.ToolHistoryPolicy)
	if err != nil {
		return nil, errKiroHTTPRequest
	}
	endpoint := url.URL{Scheme: "https", Host: "runtime." + options.Region + ".kiro.dev", Path: "/generateAssistantResponse"}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil || ctx.Err() != nil {
		return nil, errKiroHTTPRequest
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	return request, nil
}
