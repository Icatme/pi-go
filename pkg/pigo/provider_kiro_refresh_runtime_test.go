package pigo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// This deliberately supplies a caller-owned source, not a production cache or
// auto-refresh hook. Private helpers construct fixtures/encode synthetic frames; operations use public APIs.
func TestKiroPublicRefreshCallerOwnedRuntimeHandoff(t *testing.T) {
	for _, family := range []string{"idc", "desktop"} {
		for _, mode := range []string{"valid-then-expired", "expired", "scope-mismatch"} {
			t.Run(family+"/"+mode, func(t *testing.T) {
				config, model := kiroRegistrationFixture(t)
				scope := KiroCredentialScope{Region: config.Region, ProfileARN: config.ProfileARN}
				_, start := kiroRefreshFixture()
				now := start
				refreshCalls := 0
				refreshExecutor := func(request *http.Request) (*http.Response, error) {
					refreshCalls++
					if request.Header.Get("Authorization") != "" || (family == "idc" && request.URL.Path != "/token") || (family == "desktop" && request.URL.Path != "/refreshToken") {
						t.Error("refresh routing/auth")
					}
					return kiroCatalogHTTPFixture(io.NopCloser(strings.NewReader(`{"accessToken":"LOCAL_REFRESHED_ACCESS","refreshToken":"LOCAL_ROTATED_REFRESH","expiresIn":60}`))), nil
				}
				clock := func(context.Context) (time.Time, error) { return start, nil }
				var evidence interface {
					Credential() KiroScopedCredential
					ExpiresAt() time.Time
				}
				if family == "idc" {
					result, err := RefreshKiroIDC(context.Background(), KiroIDCRefreshConfig{Scope: scope, UserAgent: "local-test/1", ClientID: "LOCAL_CLIENT", ClientSecret: "LOCAL_CLIENT_SECRET", RefreshToken: "LOCAL_OLD_REFRESH", MaxRequestBytes: 4096, MaxResponseBytes: 4096, MaxLifetime: time.Hour, Clock: clock, RoundTrip: refreshExecutor})
					if err != nil {
						t.Fatal(err)
					}
					evidence = result
				} else {
					result, err := RefreshKiroDesktop(context.Background(), KiroDesktopRefreshConfig{Scope: scope, UserAgent: "local-test/1", RefreshToken: "LOCAL_OLD_REFRESH", MaxRequestBytes: 4096, MaxResponseBytes: 4096, MaxLifetime: time.Hour, Clock: clock, RoundTrip: refreshExecutor})
					if err != nil {
						t.Fatal(err)
					}
					evidence = result
				}
				if mode == "scope-mismatch" {
					config.ProfileARN += "-other"
				}
				if mode == "expired" {
					now = evidence.ExpiresAt()
				}
				sourceCalls, runtimeCalls := 0, 0
				config.Credentials = func(context.Context, KiroCredentialScope) (KiroScopedCredential, error) {
					sourceCalls++
					// Expiry checking is explicitly caller policy; the registry has no implicit
					// token lifetime metadata and must not silently renew a source on rejection.
					if !now.Before(evidence.ExpiresAt()) {
						return KiroScopedCredential{}, errors.New("LOCAL_EXPIRED")
					}
					return evidence.Credential(), nil
				}
				config.RoundTrip = func(request *http.Request) (*http.Response, error) {
					runtimeCalls++
					if request.Header.Get("Authorization") != "Bearer LOCAL_REFRESHED_ACCESS" || request.URL.Path != "/generateAssistantResponse" {
						t.Error("runtime access-token handoff")
					}
					raw, err := io.ReadAll(request.Body)
					if err != nil {
						return nil, err
					}
					for _, secret := range []string{"LOCAL_OLD_REFRESH", "LOCAL_ROTATED_REFRESH", "LOCAL_CLIENT_SECRET", "LOCAL_REFRESHED_ACCESS"} {
						if strings.Contains(request.URL.String(), secret) {
							t.Error("secret in runtime URL")
						}
						if bytes.Contains(raw, []byte(secret)) {
							t.Error("secret in runtime body")
						}
					}
					for key, values := range request.Header {
						for _, value := range values {
							if !strings.EqualFold(key, "Authorization") && strings.Contains(value, "LOCAL_REFRESHED_ACCESS") {
								t.Error("access token outside Authorization")
							}
							if strings.Contains(value, "LOCAL_OLD_REFRESH") || strings.Contains(value, "LOCAL_ROTATED_REFRESH") || strings.Contains(value, "LOCAL_CLIENT_SECRET") {
								t.Error("refresh secret in runtime headers", key)
							}
						}
					}
					return kiroAttemptResponse(io.NopCloser(bytes.NewReader(kiroOfflineJoin(kiroOfflineEvent("assistantResponseEvent", `{"content":"answer"}`), kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`))))), nil
				}
				if err := RegisterKiroBufferedModule(t.Name(), config); err != nil {
					t.Fatal(err)
				}
				defer UnregisterAPIModules(t.Name())
				if sourceCalls != 0 || runtimeCalls != 0 || refreshCalls != 1 {
					t.Fatal("registration ran credentials or refresh")
				}
				invoke := func() AssistantMessage {
					stream := StreamSimple(model, Context{Messages: []Message{UserMessage{Content: "hello"}}}, SimpleStreamOptions{RequestContext: context.Background()})
					for range stream.Events() {
					}
					result := stream.Result()
					stream.Close()
					return result
				}
				result := invoke()
				if mode == "valid-then-expired" {
					if result.StopReason != StopReasonStop || runtimeCalls != 1 || sourceCalls != 1 {
						t.Fatal("valid handoff", result.StopReason)
					}
					now = evidence.ExpiresAt()
					result = invoke()
					if result.StopReason != StopReasonError || runtimeCalls != 1 || sourceCalls != 2 {
						t.Fatal("expired source renewed or sent")
					}
				} else if result.StopReason != StopReasonError || runtimeCalls != 0 || sourceCalls != 1 {
					t.Fatal("invalid source sent", result.StopReason, runtimeCalls, sourceCalls)
				}
				if refreshCalls != 1 {
					t.Fatal("implicit re-refresh")
				}
			})
		}
	}
}
