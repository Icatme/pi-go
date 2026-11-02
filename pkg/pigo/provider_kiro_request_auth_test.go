package pigo

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestKiroRequestAuthExplicitInjection(t *testing.T) {
	conversation := Context{Messages: []Message{UserMessage{Content: "fixture"}}}
	options := kiroHTTPRequestFixture()
	for _, tc := range []struct {
		kind             kiroCredentialKind
		token, tokenType string
	}{
		{kiroCredentialBearer, "LOCAL_ONLY_INJECTED_BEARER", ""},
		{kiroCredentialAPIKey, "ksk_LOCAL_ONLY_INJECTED_FAKE_KEY", "API_KEY"},
		{kiroCredentialExternalIDP, "LOCAL_ONLY_INJECTED_EXTERNAL", "EXTERNAL_IDP"},
	} {
		request, err := newKiroOfflineRequestWithToken(context.Background(), conversation, nil, options, tc.kind, tc.token)
		if err != nil {
			t.Fatal("fixture construction failed")
		}
		if request.Header.Get("Authorization") != "Bearer "+tc.token || request.Header.Get("Tokentype") != tc.tokenType {
			t.Fatal("explicit credentials not preserved")
		}
		body, err := io.ReadAll(request.Body)
		request.Body.Close()
		if err != nil || bytes.Contains(body, []byte(tc.token)) || strings.Contains(request.URL.String(), tc.token) {
			t.Fatal("credential leaked outside auth headers")
		}
		if request.Header.Get("User-Agent") != "pi-go/offline-fixture" {
			t.Fatal("authentication rewrote client identity")
		}
	}
	// A separate unauthenticated construction never inherits previous secrets.
	plain, err := newKiroOfflineHTTPRequest(context.Background(), conversation, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Body.Close()
	if plain.Header.Get("Authorization") != "" || plain.Header.Get("Tokentype") != "" {
		t.Fatal("credential persisted between calls")
	}
}

func TestKiroRequestAuthFailureReturnsNoRequestOrSecret(t *testing.T) {
	const sentinel = "LOCAL_ONLY_SENTINEL_NO_ERROR_ECHO"
	conversation := Context{Messages: []Message{UserMessage{Content: "fixture"}}}
	options := kiroHTTPRequestFixture()
	request, err := newKiroOfflineRequestWithToken(context.Background(), conversation, nil, options, kiroCredentialAPIKey, sentinel)
	if err == nil || request != nil || strings.Contains(err.Error(), sentinel) {
		t.Fatal("invalid auth leaked partial request/input")
	}
	options.Region = "evil.invalid"
	request, err = newKiroOfflineRequestWithToken(context.Background(), conversation, nil, options, kiroCredentialBearer, sentinel)
	if err == nil || request != nil || strings.Contains(err.Error(), sentinel) {
		t.Fatal("invalid endpoint leaked partial request/input")
	}
	options = kiroHTTPRequestFixture()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	request, err = newKiroOfflineRequestWithToken(cancelled, conversation, nil, options, kiroCredentialBearer, sentinel)
	if err == nil || request != nil || strings.Contains(err.Error(), sentinel) {
		t.Fatal("cancelled construction leaked request/input")
	}
	request, err = newKiroOfflineRequestWithToken(nil, conversation, nil, options, kiroCredentialBearer, sentinel)
	if err == nil || request != nil {
		t.Fatal("nil context accepted")
	}
}
