package pigo

import (
	"reflect"
	"strings"
	"testing"
)

func TestKiroClientHeadersExplicitCompatibility(t *testing.T) {
	ua, err := kiroCommunityCLIUserAgent("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	expectedUA := "aws-sdk-rust/1.0.0 ua/2.1 os/other lang/rust api/codewhispererstreaming#1.28.3 m/E app/AmazonQ-For-CLI md/appVersion-1.28.3-0123456789abcdef0123456789abcdef"
	if ua != expectedUA {
		t.Fatalf("compatibility template changed: %q", ua)
	}
	id := "01234567-89ab-cdef-0123-456789abcdef"
	headers, err := buildKiroOfflineClientHeaders(ua, id)
	expected := map[string]string{
		"Content-Type": "application/json", "Accept": "application/vnd.amazon.eventstream",
		"x-amzn-codewhisperer-optout": "true", "amz-sdk-invocation-id": id,
		"amz-sdk-request": "attempt=1; max=1", "x-amzn-kiro-agent-mode": "vibe",
		"x-amz-user-agent": expectedUA, "user-agent": expectedUA,
	}
	if err != nil || !reflect.DeepEqual(headers, expected) {
		t.Fatalf("header contract: %#v %v", headers, err)
	}
	for k := range headers {
		if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "tokentype") {
			t.Fatal("authentication silently added")
		}
	}
}

func TestKiroClientHeadersDeclaredIdentityAndOwnership(t *testing.T) {
	id := "00000000-0000-0000-0000-000000000000"
	first, err := buildKiroOfflineClientHeaders("pi-go/fixture", id)
	if err != nil || first["user-agent"] != "pi-go/fixture" || first["x-amz-user-agent"] != "pi-go/fixture" {
		t.Fatalf("identity rewritten: %#v %v", first, err)
	}
	first["user-agent"] = "changed"
	first["Authorization"] = "fake-local-value"
	second, err := buildKiroOfflineClientHeaders("pi-go/fixture", id)
	if err != nil || second["user-agent"] != "pi-go/fixture" || second["Authorization"] != "" {
		t.Fatal("maps alias or input retained")
	}
}

func TestKiroClientHeadersRejectInvalidInputs(t *testing.T) {
	validID := "01234567-89ab-cdef-0123-456789abcdef"
	for _, ua := range []string{"", " spaced", "spaced ", "a\r\nInjected: yes", "a\tb", "a\x00b", "a\x7fb", "非ASCII", strings.Repeat("a", 513), string([]byte{255})} {
		if h, err := buildKiroOfflineClientHeaders(ua, validID); err == nil || h != nil {
			t.Fatalf("invalid UA accepted: %q", ua)
		}
	}
	for _, id := range []string{"", "0123456789abcdef0123456789abcdef", "01234567_89ab-cdef-0123-456789abcdef", "01234567-89ab-cdef-0123-456789abcdeg", "01234567-89AB-cdef-0123-456789abcdef", validID + "\n"} {
		if h, err := buildKiroOfflineClientHeaders("pi-go/fixture", id); err == nil || h != nil {
			t.Fatalf("invalid invocation ID accepted: %q", id)
		}
	}
	for _, id := range []string{"", strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("G", 32), validID} {
		if ua, err := kiroCommunityCLIUserAgent(id); err == nil || ua != "" {
			t.Fatalf("invalid compatibility ID: %q", id)
		}
	}
	// Upper accepted UA boundary; no truncation or template substitution.
	ua := strings.Repeat("a", 512)
	if h, err := buildKiroOfflineClientHeaders(ua, validID); err != nil || h["user-agent"] != ua {
		t.Fatal("UA boundary changed")
	}
}
