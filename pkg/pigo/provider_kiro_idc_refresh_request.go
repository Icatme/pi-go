package pigo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

var errKiroIDCRefreshRequest = errors.New("kiro: invalid offline IDC refresh request")

type kiroIDCRefreshRequestOptions struct {
	Region       string
	UserAgent    string
	ClientID     string `json:"-"`
	ClientSecret string `json:"-"`
	RefreshToken string `json:"-"`
	MaxBytes     int
}

func (kiroIDCRefreshRequestOptions) String() string   { return "[kiro IDC refresh request options]" }
func (kiroIDCRefreshRequestOptions) GoString() string { return "[kiro IDC refresh request options]" }

// newKiroOfflineIDCRefreshRequest only constructs a standard-partition AWS OIDC
// refresh request from explicit caller inputs. It never executes, discovers,
// registers a client, refreshes a source or persists secrets. Region syntax does
// not establish endpoint availability or client/token authorization. Other AWS
// partitions are not guessed. No CLI identity or bearer header is added.
// The returned request/body contains secrets: never log, dump or persist it.
// GetBody is deliberately absent; there is no retry/redirect permission here.
func newKiroOfflineIDCRefreshRequest(ctx context.Context, options kiroIDCRefreshRequestOptions) (*http.Request, error) {
	if ctx == nil || ctx.Err() != nil || options.MaxBytes < 1 || options.MaxBytes > 1<<20 || len(options.Region) > 63 || !kiroRuntimeRegionShape.MatchString(options.Region) || strings.HasPrefix(options.Region, "cn-") || strings.HasPrefix(options.Region, "us-gov-") || strings.Contains(options.Region, "-iso") || !kiroExplicitUserAgentValid(options.UserAgent) || !kiroCatalogLabel(options.ClientID, 16384) || !kiroCatalogLabel(options.ClientSecret, 16384) || !kiroCatalogLabel(options.RefreshToken, 16384) {
		return nil, errKiroIDCRefreshRequest
	}
	body, err := json.Marshal(struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
		RefreshToken string `json:"refreshToken"`
		GrantType    string `json:"grantType"`
	}{options.ClientID, options.ClientSecret, options.RefreshToken, "refresh_token"})
	if err != nil || len(body) > options.MaxBytes || ctx.Err() != nil {
		return nil, errKiroIDCRefreshRequest
	}
	endpoint := url.URL{Scheme: "https", Host: "oidc." + options.Region + ".amazonaws.com", Path: "/token"}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil || ctx.Err() != nil {
		return nil, errKiroIDCRefreshRequest
	}
	request.GetBody = nil
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", options.UserAgent)
	return request, nil
}
