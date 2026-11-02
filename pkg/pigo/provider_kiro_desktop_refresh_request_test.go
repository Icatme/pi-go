package pigo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

func kiroDesktopRefreshRequestFixture() kiroDesktopRefreshRequestOptions {
	scope, _ := kiroRefreshFixture()
	return kiroDesktopRefreshRequestOptions{Scope: scope, UserAgent: "local-test/1", RefreshToken: `PRIVATE_REFRESH&"<>`, MaxBytes: 4096}
}

func TestKiroDesktopRefreshRequestExactCommunityShape(t *testing.T) {
	options := kiroDesktopRefreshRequestFixture()
	request, err := newKiroOfflineDesktopRefreshRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer request.Body.Close()
	if request.Method != "POST" || request.URL.String() != "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken" || request.GetBody != nil || request.Header.Get("User-Agent") != "local-test/1" || request.Header.Get("Content-Type") != "application/json" || len(request.Header) != 3 {
		t.Fatal("request shape")
	}
	raw, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	if json.Unmarshal(raw, &body) != nil || len(body) != 1 || body["refreshToken"] != options.RefreshToken || request.ContentLength != int64(len(raw)) {
		t.Fatal("body mismatch")
	}
	for _, s := range []string{request.URL.String(), fmt.Sprint(request.Header), fmt.Sprintf("%v", options), fmt.Sprintf("%+v", options), fmt.Sprintf("%#v", options)} {
		if strings.Contains(s, "PRIVATE_REFRESH") {
			t.Fatal("secret outside explicit body")
		}
	}
	encoded, err := json.Marshal(options)
	if err != nil || strings.Contains(string(encoded), "PRIVATE_REFRESH") {
		t.Fatal("option secret JSON")
	}
	options.MaxBytes = len(raw)
	exact, err := newKiroOfflineDesktopRefreshRequest(context.Background(), options)
	if err != nil {
		t.Fatal("exact budget")
	}
	exact.Body.Close()
	options.MaxBytes--
	if request, err := newKiroOfflineDesktopRefreshRequest(context.Background(), options); err == nil || request != nil {
		t.Fatal("escaped byte budget")
	}
}

func TestKiroDesktopRefreshRequestRejectsInvalidRoutingAndInput(t *testing.T) {
	for _, change := range []func(*kiroDesktopRefreshRequestOptions){func(o *kiroDesktopRefreshRequestOptions) { o.Scope.Region = "us-east-1.evil.test" }, func(o *kiroDesktopRefreshRequestOptions) { o.Scope.Region = "https://example.invalid" }, func(o *kiroDesktopRefreshRequestOptions) { o.Scope.ProfileARN = "" }, func(o *kiroDesktopRefreshRequestOptions) { o.RefreshToken = "" }, func(o *kiroDesktopRefreshRequestOptions) { o.RefreshToken = "PRIVATE\nTOKEN" }, func(o *kiroDesktopRefreshRequestOptions) { o.RefreshToken = string([]byte{0xff}) }, func(o *kiroDesktopRefreshRequestOptions) { o.RefreshToken = strings.Repeat("x", 16385) }, func(o *kiroDesktopRefreshRequestOptions) { o.UserAgent = "" }, func(o *kiroDesktopRefreshRequestOptions) { o.UserAgent = "desktop\r\nInjected: x" }, func(o *kiroDesktopRefreshRequestOptions) { o.MaxBytes = 0 }, func(o *kiroDesktopRefreshRequestOptions) { o.MaxBytes = (1 << 20) + 1 }} {
		options := kiroDesktopRefreshRequestFixture()
		change(&options)
		request, err := newKiroOfflineDesktopRefreshRequest(context.Background(), options)
		if request != nil || err != errKiroDesktopRefreshRequest {
			t.Fatal("invalid input accepted")
		}
	}
}

func TestKiroDesktopRefreshRequestContextAndInputSnapshot(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, cancelled} {
		request, err := newKiroOfflineDesktopRefreshRequest(ctx, kiroDesktopRefreshRequestFixture())
		if request != nil || err == nil {
			t.Fatal("invalid context")
		}
	}
	options := kiroDesktopRefreshRequestFixture()
	original := options.RefreshToken
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := newKiroOfflineDesktopRefreshRequest(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	defer request.Body.Close()
	options.RefreshToken = "CHANGED"
	options.Scope.Region = "eu-west-1"
	raw, _ := io.ReadAll(request.Body)
	var body map[string]string
	_ = json.Unmarshal(raw, &body)
	if body["refreshToken"] != original || request.URL.Host != "prod.us-east-1.auth.desktop.kiro.dev" {
		t.Fatal("input alias")
	}
	cancel()
	if request.Context().Err() != context.Canceled {
		t.Fatal("context lost")
	}
}
