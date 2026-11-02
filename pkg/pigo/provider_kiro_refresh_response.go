package pigo

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

var errKiroRefreshResponse = errors.New("kiro: invalid offline refresh response")

type kiroRefreshFamily string

const (
	kiroRefreshIDC         kiroRefreshFamily = "idc"
	kiroRefreshDesktop     kiroRefreshFamily = "desktop"
	kiroRefreshExternalIDP kiroRefreshFamily = "external_idp"
)

// Contains secrets in memory; JSON/fmt forms must not expose access/refresh data.
// Scope and family are caller assertions, not proof of account/token ownership.
type kiroRefreshResult struct {
	Credential   kiroScopedCredential `json:"-"`
	RefreshToken string               `json:"-"`
	Family       kiroRefreshFamily
	ExpiresAt    time.Time
	RefreshAfter time.Time
}

func (kiroRefreshResult) String() string   { return "[kiro refresh result]" }
func (kiroRefreshResult) GoString() string { return "[kiro refresh result]" }

// parseKiroOfflineRefreshResponse parses supplied successful-response JSON only.
// It neither obtains credentials, validates HTTP status, calls an endpoint,
// changes a source, persists tokens nor grants entitlement. No default clock or
// lifetime is guessed. maxLifetime is caller policy (whole seconds, <=30 days),
// not an assertion about service expiry; refreshSkew cannot extend ExpiresAt.
func parseKiroOfflineRefreshResponse(payload []byte, scope kiroCredentialScope, family kiroRefreshFamily, previousRefresh string, now time.Time, maxLifetime, refreshSkew time.Duration, maxBytes int) (kiroRefreshResult, error) {
	zero := kiroRefreshResult{}
	if maxBytes < 1 || maxBytes > 1<<20 || now.IsZero() || now.Year() < 1 || now.Year() > 9998 || maxLifetime < time.Second || maxLifetime > 30*24*time.Hour || maxLifetime%time.Second != 0 || refreshSkew < 0 || refreshSkew > maxLifetime || len(scope.Region) > 63 || !kiroRuntimeRegionShape.MatchString(scope.Region) || !kiroCatalogLabel(scope.ProfileARN, 2048) || !kiroCatalogLabel(previousRefresh, 16384) {
		return zero, errKiroRefreshResponse
	}
	accessField, refreshField, expiresField, tokenTypeField := "accessToken", "refreshToken", "expiresIn", "tokenType"
	kind := kiroCredentialBearer
	switch family {
	case kiroRefreshIDC, kiroRefreshDesktop:
	case kiroRefreshExternalIDP:
		accessField, refreshField, expiresField, tokenTypeField = "access_token", "refresh_token", "expires_in", "token_type"
		kind = kiroCredentialExternalIDP
	default:
		return zero, errKiroRefreshResponse
	}
	fields, err := decodeKiroJSONObject(payload, maxBytes)
	if err != nil {
		return zero, errKiroRefreshResponse
	}
	for field := range fields {
		if field != accessField && field != refreshField && field != expiresField && field != tokenTypeField && !(family == kiroRefreshDesktop && field == "profileArn") {
			return zero, errKiroRefreshResponse
		}
	}
	var access string
	if json.Unmarshal(fields[accessField], &access) != nil {
		return zero, errKiroRefreshResponse
	}
	if _, err := buildKiroExplicitAuthHeaders(kind, access); err != nil {
		return zero, errKiroRefreshResponse
	}
	if raw, ok := fields[tokenTypeField]; ok {
		var tokenType string
		if json.Unmarshal(raw, &tokenType) != nil || !strings.EqualFold(tokenType, "Bearer") {
			return zero, errKiroRefreshResponse
		}
	}
	refresh := previousRefresh
	if raw, ok := fields[refreshField]; ok {
		var rotated string
		if json.Unmarshal(raw, &rotated) != nil || !kiroCatalogLabel(rotated, 16384) {
			return zero, errKiroRefreshResponse
		}
		refresh = rotated
	} else if family == kiroRefreshIDC {
		return zero, errKiroRefreshResponse
	}
	if raw, ok := fields["profileArn"]; ok {
		var profile string
		if json.Unmarshal(raw, &profile) != nil || profile != scope.ProfileARN {
			return zero, errKiroRefreshResponse
		}
	}
	literal := bytes.TrimSpace(fields[expiresField])
	if len(literal) == 0 {
		return zero, errKiroRefreshResponse
	}
	for _, digit := range literal {
		if digit < '0' || digit > '9' {
			return zero, errKiroRefreshResponse
		}
	}
	seconds, err := strconv.ParseInt(string(literal), 10, 64)
	if err != nil || seconds < 1 || seconds > int64(maxLifetime/time.Second) {
		return zero, errKiroRefreshResponse
	}
	lifetime := time.Duration(seconds) * time.Second
	expiresAt := now.UTC().Add(lifetime)
	refreshAfter := expiresAt.Add(-refreshSkew)
	if refreshAfter.Before(now) {
		refreshAfter = now.UTC()
	}
	return kiroRefreshResult{Credential: kiroScopedCredential{Scope: scope, Kind: kind, Token: access}, RefreshToken: refresh, Family: family, ExpiresAt: expiresAt, RefreshAfter: refreshAfter}, nil
}
