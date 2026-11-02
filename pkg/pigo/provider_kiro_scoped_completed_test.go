package pigo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
)

func TestKiroScopedCompletedFreshCredentialAndBothTerminals(t *testing.T) {
	conversation, schemas, config := kiroInjectedToolFixture()
	sourceCalls, httpCalls := 0, 0
	source := func(ctx context.Context, scope kiroCredentialScope) (kiroScopedCredential, error) {
		sourceCalls++
		if scope.ProfileARN != config.Request.Identity.ProfileARN || scope.Region != config.Request.Region {
			t.Fatal("scope")
		}
		token := "FIRST_LOCAL_ONLY"
		if sourceCalls == 2 {
			token = "SECOND_LOCAL_ONLY"
		}
		return kiroScopedCredential{Scope: scope, Kind: kiroCredentialBearer, Token: token}, nil
	}
	for i := 0; i < 2; i++ {
		body := &kiroAttemptBody{reader: bytes.NewReader(kiroInjectedToolWire())}
		if i == 1 {
			body.reader = bytes.NewReader(kiroOfflineJoin(kiroOfflineEvent("assistantResponseEvent", `{"content":"done"}`), kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`)))
		}
		stream, err := runKiroScopedCompletedStream(context.Background(), conversation, schemas, config, source, func(request *http.Request) (*http.Response, error) {
			httpCalls++
			expected := "Bearer FIRST_LOCAL_ONLY"
			if i == 1 {
				expected = "Bearer SECOND_LOCAL_ONLY"
			}
			raw, _ := io.ReadAll(request.Body)
			if request.Header.Get("Authorization") != expected || bytes.Contains(raw, []byte("LOCAL_ONLY")) {
				t.Fatal("credential ownership")
			}
			return kiroAttemptResponse(body), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		result := stream.Result()
		for range stream.Events() {
		}
		stream.Close()
		expected := StopReasonToolUse
		if i == 1 {
			expected = StopReasonStop
		}
		if result.StopReason != expected || body.closes != 1 {
			t.Fatal("terminal/close")
		}
	}
	if sourceCalls != 2 || httpCalls != 2 {
		t.Fatal("source cached or attempt repeated")
	}
}

func TestKiroScopedCompletedPreflightBeforeCredentials(t *testing.T) {
	for _, scenario := range []string{"nil-context", "cancelled", "budget", "model", "unsupported-context", "nil-executor", "nil-source"} {
		t.Run(scenario, func(t *testing.T) {
			conversation, schemas, config := kiroInjectedToolFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var requestCtx context.Context = ctx
			sourceCalls, httpCalls := 0, 0
			source := kiroCredentialSource(func(context.Context, kiroCredentialScope) (kiroScopedCredential, error) {
				sourceCalls++
				return kiroScopedCredential{}, nil
			})
			executor := func(*http.Request) (*http.Response, error) { httpCalls++; return nil, nil }
			switch scenario {
			case "nil-context":
				requestCtx = nil
			case "cancelled":
				cancel()
			case "budget":
				config.MaxEventBytes = 1
			case "model":
				config.Model.ID = "different"
			case "unsupported-context":
				conversation.SystemPrompt = "must not silently ignore"
			case "nil-executor":
				executor = nil
			case "nil-source":
				source = nil
			}
			stream, err := runKiroScopedCompletedStream(requestCtx, conversation, schemas, config, source, executor)
			if stream != nil || err != errKiroInjectedToolStream || sourceCalls != 0 || httpCalls != 0 {
				t.Fatal("preflight order")
			}
		})
	}
}

func TestKiroScopedCompletedFailuresNeverReleaseTools(t *testing.T) {
	for _, scenario := range []string{"source-error", "scope", "cancel", "token", "read", "close", "transport"} {
		t.Run(scenario, func(t *testing.T) {
			conversation, schemas, config := kiroInjectedToolFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sourceCalls, httpCalls := 0, 0
			body := &kiroAttemptBody{reader: bytes.NewReader(kiroInjectedToolWire())}
			source := func(_ context.Context, scope kiroCredentialScope) (kiroScopedCredential, error) {
				sourceCalls++
				credential := kiroScopedCredential{Scope: scope, Kind: kiroCredentialBearer, Token: "LOCAL_ONLY"}
				switch scenario {
				case "source-error":
					return credential, errors.New("PRIVATE_SOURCE_ERROR")
				case "scope":
					credential.Scope.Region = "different"
				case "cancel":
					cancel()
				case "token":
					credential.Token = "bad\r\nsecret"
				}
				return credential, nil
			}
			if scenario == "read" {
				wire := kiroInjectedToolWire()
				body.reader = bytes.NewReader(wire[:len(wire)-1])
			}
			if scenario == "close" {
				body.closeErr = errors.New("PRIVATE_CLOSE_ERROR")
			}
			stream, err := runKiroScopedCompletedStream(ctx, conversation, schemas, config, source, func(*http.Request) (*http.Response, error) {
				httpCalls++
				if scenario == "transport" {
					return kiroAttemptResponse(body), errors.New("PRIVATE_TRANSPORT_ERROR")
				}
				return kiroAttemptResponse(body), nil
			})
			expectedHTTP := 0
			if scenario == "read" || scenario == "close" || scenario == "transport" {
				expectedHTTP = 1
			}
			if stream != nil || err != errKiroInjectedToolStream || sourceCalls != 1 || httpCalls != expectedHTTP || body.closes != expectedHTTP {
				t.Fatal("failure leaked stream/retried/not closed")
			}
		})
	}
}

func TestKiroScopedCompletedSnapshotsBeforeSource(t *testing.T) {
	conversation, schemas, config := kiroInjectedToolFixture()
	source := func(_ context.Context, scope kiroCredentialScope) (kiroScopedCredential, error) {
		conversation.Tools[0].Name = "changed_by_source"
		conversation.Messages[0] = UserMessage{Content: "changed_by_source"}
		schemas["lookup"] = []byte(`{"type":"string"}`)
		return kiroScopedCredential{Scope: scope, Kind: kiroCredentialBearer, Token: "LOCAL_ONLY"}, nil
	}
	stream, err := runKiroScopedCompletedStream(context.Background(), conversation, schemas, config, source, func(request *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(request.Body)
		if bytes.Contains(raw, []byte("changed_by_source")) || !bytes.Contains(raw, []byte(`"lookup"`)) {
			t.Fatal("request was not snapshotted")
		}
		return kiroAttemptResponse(io.NopCloser(bytes.NewReader(kiroInjectedToolWire()))), nil
	})
	if err != nil {
		t.Fatal("allowlist changed after source", err)
	}
	defer stream.Close()
	result := stream.Result()
	if result.StopReason != StopReasonToolUse || result.Content[0].(ToolCall).Name != "lookup" {
		t.Fatal("allowlist/result")
	}
	for range stream.Events() {
	}
}
