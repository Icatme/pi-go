package pigo

import (
	"errors"
	"strings"
)

var errKiroExplicitAuth = errors.New("kiro: invalid explicit authentication input")

type kiroCredentialKind string
const (
	kiroCredentialBearer kiroCredentialKind = "bearer"
	kiroCredentialAPIKey kiroCredentialKind = "api_key"
	kiroCredentialExternalIDP kiroCredentialKind = "external_idp"
)

// buildKiroExplicitAuthHeaders creates sensitive headers from explicit inputs.
// It never discovers/refreshes/persists credentials or infers token authority from
// unsigned JWT claims. No caller input is included in errors. Returned headers
// CONTAIN THE SECRET: do not log, serialize for diagnostics or attach to errors.
// Kind is caller asserted, not validated entitlement or cryptographic identity.
func buildKiroExplicitAuthHeaders(kind kiroCredentialKind, token string) (map[string]string, error) {
	if kind != kiroCredentialBearer && kind != kiroCredentialAPIKey && kind != kiroCredentialExternalIDP {
		return nil, errKiroExplicitAuth
	}
	if len(token) == 0 || len(token) > 16384 { return nil, errKiroExplicitAuth }
	// Conservative RFC 6750 b64token character subset, with '=' only as
	// trailing padding. This is not signature/claims/expiration validation.
	padding := false
	for i, c := range token {
		if c == '=' {
			if i == 0 { return nil, errKiroExplicitAuth }
			padding = true
			continue
		}
		if padding || !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("-._~+/", c)) {
			return nil, errKiroExplicitAuth
		}
	}
	isKey := strings.HasPrefix(token, "ksk_")
	if (kind == kiroCredentialAPIKey && (!isKey || len(token) == 4)) || (kind != kiroCredentialAPIKey && isKey) {
		return nil, errKiroExplicitAuth
	}
	headers := map[string]string{"Authorization": "Bearer " + token}
	switch kind {
	case kiroCredentialAPIKey:
		headers["tokentype"] = "API_KEY"
	case kiroCredentialExternalIDP:
		headers["tokentype"] = "EXTERNAL_IDP"
	}
	return headers, nil
}
