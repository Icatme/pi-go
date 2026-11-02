package pigo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func kiroRefreshFixture() (kiroCredentialScope, time.Time) {
	return kiroCredentialScope{Region: "us-east-1", ProfileARN: "explicit-profile"}, time.Date(2026, 11, 2, 5, 0, 0, 0, time.UTC)
}

func TestKiroRefreshResponseRotationAndSeparateExpiry(t *testing.T) {
	for _, test := range []struct {
		family         kiroRefreshFamily
		body, expected string
		kind           kiroCredentialKind
	}{
		{kiroRefreshIDC, `{"accessToken":"NEW_ACCESS","refreshToken":"NEW_REFRESH","expiresIn":3600}`, "NEW_REFRESH", kiroCredentialBearer},
		{kiroRefreshDesktop, `{"accessToken":"NEW_ACCESS","expiresIn":3600,"profileArn":"explicit-profile"}`, "OLD_REFRESH", kiroCredentialBearer},
		{kiroRefreshExternalIDP, `{"access_token":"NEW_ACCESS","refresh_token":"NEW_REFRESH","expires_in":3600,"token_type":"Bearer"}`, "NEW_REFRESH", kiroCredentialExternalIDP},
		{kiroRefreshExternalIDP, `{"access_token":"NEW_ACCESS","expires_in":3600}`, "OLD_REFRESH", kiroCredentialExternalIDP},
	} {
		t.Run(string(test.family)+test.expected, func(t *testing.T) {
			scope, now := kiroRefreshFixture()
			payload := []byte(test.body)
			result, err := parseKiroOfflineRefreshResponse(payload, scope, test.family, "OLD_REFRESH", now, 24*time.Hour, 5*time.Minute, 4096)
			if err != nil || result.Credential.Scope != scope || result.Credential.Token != "NEW_ACCESS" || result.Credential.Kind != test.kind || result.RefreshToken != test.expected || result.Family != test.family || !result.ExpiresAt.Equal(now.Add(time.Hour)) || !result.RefreshAfter.Equal(now.Add(55*time.Minute)) {
				t.Fatal("refresh result", err)
			}
			for i := range payload {
				payload[i] = 'x'
			}
			if result.Credential.Token != "NEW_ACCESS" || result.RefreshToken != test.expected {
				t.Fatal("borrowed payload")
			}
		})
	}
}

func TestKiroRefreshResponseRejectsMalformedOrAmbiguousEvidence(t *testing.T) {
	scope, now := kiroRefreshFixture()
	for _, raw := range []string{
		`{}`, `{"accessToken":"NEW_ACCESS","refreshToken":"NEW_REFRESH"}`, `{"accessToken":null,"refreshToken":"NEW_REFRESH","expiresIn":3600}`,
		`{"accessToken":"NEW_ACCESS","refreshToken":"NEW_REFRESH","expiresIn":0}`, `{"accessToken":"NEW_ACCESS","refreshToken":"NEW_REFRESH","expiresIn":-1}`,
		`{"accessToken":"NEW_ACCESS","refreshToken":"NEW_REFRESH","expiresIn":1.0}`, `{"accessToken":"NEW_ACCESS","refreshToken":"NEW_REFRESH","expiresIn":1e3}`,
		`{"accessToken":"NEW_ACCESS","refreshToken":"NEW_REFRESH","expiresIn":"3600"}`, `{"accessToken":"NEW_ACCESS","refreshToken":"NEW_REFRESH","expiresIn":9223372036854775808}`,
		`{"accessToken":"NEW_ACCESS","refreshToken":"NEW_REFRESH","expiresIn":86401}`, `{"accessToken":"NEW_ACCESS","refreshToken":null,"expiresIn":3600}`,
		`{"accessToken":"NEW_ACCESS","refreshToken":"","expiresIn":3600}`, `{"accessToken":"NEW_ACCESS","expiresIn":3600}`,
		`{"accessToken":"NEW_ACCESS","refreshToken":"NEW_REFRESH","expiresIn":3600,"expiresIn":1}`, `{"accessToken":"PRIVATE\nBAD","refreshToken":"NEW_REFRESH","expiresIn":3600}`,
		`{"accessToken":"ksk_NOT_REFRESHABLE","refreshToken":"NEW_REFRESH","expiresIn":3600}`, `{"accessToken":"NEW_ACCESS","refreshToken":"NEW_REFRESH","expiresIn":3600,"tokenType":"MAC"}`,
		`{"accessToken":"NEW_ACCESS","refreshToken":"NEW_REFRESH","expiresIn":3600,"unreviewed":true}`,
	} {
		result, err := parseKiroOfflineRefreshResponse([]byte(raw), scope, kiroRefreshIDC, "OLD_REFRESH", now, 24*time.Hour, 5*time.Minute, 4096)
		if err != errKiroRefreshResponse || !reflect.DeepEqual(result, kiroRefreshResult{}) || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal("malformed evidence accepted")
		}
	}
	for _, family := range []kiroRefreshFamily{kiroRefreshDesktop, kiroRefreshExternalIDP} {
		raw := `{"accessToken":"NEW_ACCESS","refreshToken":null,"expiresIn":3600}`
		if family == kiroRefreshExternalIDP {
			raw = `{"access_token":"NEW_ACCESS","refresh_token":null,"expires_in":3600}`
		}
		if _, err := parseKiroOfflineRefreshResponse([]byte(raw), scope, family, "OLD_REFRESH", now, time.Hour, 0, 4096); err == nil {
			t.Fatal("explicit null silently fell back")
		}
	}
}

func TestKiroRefreshResponseScopeFamilyClockAndBudget(t *testing.T) {
	for _, mode := range []string{"family", "wrong-fields", "profile-switch", "region", "zero-clock", "late-calendar", "lifetime", "skew", "fraction-policy", "budget", "old-secret"} {
		t.Run(mode, func(t *testing.T) {
			scope, now := kiroRefreshFixture()
			family := kiroRefreshDesktop
			raw := `{"accessToken":"NEW_ACCESS","expiresIn":3600}`
			lifetime, skew := 24*time.Hour, 5*time.Minute
			budget := 4096
			old := "OLD_REFRESH"
			switch mode {
			case "family":
				family = "apikey"
			case "wrong-fields":
				family = kiroRefreshExternalIDP
			case "profile-switch":
				raw = `{"accessToken":"NEW_ACCESS","expiresIn":3600,"profileArn":"other-account"}`
			case "region":
				scope.Region = "US-EAST-1"
			case "zero-clock":
				now = time.Time{}
			case "late-calendar":
				now = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
			case "lifetime":
				lifetime = 31 * 24 * time.Hour
			case "skew":
				skew = -1
			case "fraction-policy":
				lifetime = time.Second + 1
			case "budget":
				budget = 1
			case "old-secret":
				old = "PRIVATE\nBAD"
			}
			result, err := parseKiroOfflineRefreshResponse([]byte(raw), scope, family, old, now, lifetime, skew, budget)
			if err != errKiroRefreshResponse || !reflect.DeepEqual(result, kiroRefreshResult{}) {
				t.Fatal("invalid scope/policy")
			}
		})
	}
	scope, now := kiroRefreshFixture()
	r, err := parseKiroOfflineRefreshResponse([]byte(`{"accessToken":"NEW_ACCESS","expiresIn":60}`), scope, kiroRefreshDesktop, "OLD_REFRESH", now, time.Hour, 5*time.Minute, 4096)
	if err != nil || !r.ExpiresAt.Equal(now.Add(time.Minute)) || !r.RefreshAfter.Equal(now) {
		t.Fatal("skew extended expiry or moved clock", err)
	}
}

func TestKiroRefreshResponseRedactedDiagnostics(t *testing.T) {
	scope, now := kiroRefreshFixture()
	r, err := parseKiroOfflineRefreshResponse([]byte(`{"accessToken":"UNIQUE_ACCESS_SECRET","refreshToken":"UNIQUE_REFRESH_SECRET","expiresIn":3600}`), scope, kiroRefreshIDC, "OLD_REFRESH", now, time.Hour, 0, 4096)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{string(raw), fmt.Sprintf("%v", r), fmt.Sprintf("%+v", r), fmt.Sprintf("%#v", r), fmt.Sprintf("%#v", &r)} {
		if strings.Contains(s, "UNIQUE_") || bytes.Contains([]byte(s), []byte("OLD_REFRESH")) {
			t.Fatal("credential in diagnostics")
		}
	}
}
