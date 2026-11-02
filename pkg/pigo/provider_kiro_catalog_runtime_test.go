package pigo

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

// The alias is deliberately different from the exact service ID. This test uses
// public APIs; internal framing helpers only encode synthetic response bytes.
func TestKiroCatalogExplicitServiceIDToRuntime(t *testing.T) {
	scope := KiroCredentialScope{Region: "us-east-1", ProfileARN: "local-profile"}
	now := time.Date(2026, 11, 2, 5, 0, 0, 0, time.UTC)
	source := func(_ context.Context, s KiroCredentialScope) (KiroScopedCredential, error) {
		if s != scope {
			t.Error("scope changed")
		}
		return KiroScopedCredential{Scope: s, Kind: KiroCredentialBearer, Token: "LOCAL_ONLY"}, nil
	}
	catalogCalls := 0
	catalog, err := FetchKiroModelCatalog(context.Background(), KiroCatalogFetchConfig{Scope: scope, Origin: "LOCAL_TEST", UserAgent: "local/1", MaxBytes: 4096, MaxModels: 4, Credentials: source, Clock: func(context.Context) (time.Time, error) { return now, nil }, RoundTrip: func(*http.Request) (*http.Response, error) {
		catalogCalls++
		return &http.Response{StatusCode: 200, ContentLength: -1, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewBufferString(`{"models":[{"modelId":"sample-1.2.3"},{"modelId":"sample-1-2.3"}]}`))}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	// This alias overlaps a different model's service ID. Explicit namespaces are
	// necessary; no replace-all heuristic or default model can resolve it safely.
	byAlias, err := catalog.Select(scope, KiroCatalogAlias, "sample-1-2.3", now, time.Hour)
	if err != nil || byAlias.ServiceID() != "sample-1.2.3" {
		t.Fatal("alias lookup", err)
	}
	selected, err := catalog.Select(scope, KiroCatalogServiceID, "sample-1-2.3", now, time.Hour)
	if err != nil || selected.Alias() != "sample-1-2-3" {
		t.Fatal("service lookup", err)
	}
	config, model := kiroRegistrationFixture(t)
	config.Region = scope.Region
	config.ProfileARN = scope.ProfileARN
	config.Credentials = source
	model.ID = selected.ServiceID() // Explicit caller mapping, not automatic registration.
	calls := 0
	config.RoundTrip = func(request *http.Request) (*http.Response, error) {
		calls++
		var body struct {
			ConversationState struct {
				CurrentMessage struct {
					UserInputMessage struct {
						ModelID string `json:"modelId"`
					} `json:"userInputMessage"`
				} `json:"currentMessage"`
				History []struct {
					UserInputMessage *struct {
						ModelID string `json:"modelId"`
					} `json:"userInputMessage"`
				} `json:"history"`
			} `json:"conversationState"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
			return nil, err
		}
		if body.ConversationState.CurrentMessage.UserInputMessage.ModelID != "sample-1-2.3" {
			t.Error("current service ID changed")
		}
		if len(body.ConversationState.History) != 2 || body.ConversationState.History[0].UserInputMessage == nil || body.ConversationState.History[0].UserInputMessage.ModelID != "sample-1-2.3" {
			t.Error("history service ID changed")
		}
		return kiroAttemptResponse(io.NopCloser(bytes.NewReader(kiroOfflineJoin(kiroOfflineEvent("assistantResponseEvent", `{"content":"answer"}`), kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`))))), nil
	}
	if err := RegisterKiroBufferedModule(t.Name(), config); err != nil {
		t.Fatal(err)
	}
	defer UnregisterAPIModules(t.Name())
	if calls != 0 || catalogCalls != 1 {
		t.Fatal("registration fetched catalog/runtime")
	}
	conversation := Context{Messages: []Message{UserMessage{Content: "previous"}, AssistantMessage{Content: []ContentBlock{TextContent{Text: "answer"}}, StopReason: StopReasonStop}, UserMessage{Content: "next"}}}
	stream := StreamSimple(model, conversation, SimpleStreamOptions{RequestContext: context.Background()})
	for range stream.Events() {
	}
	result := stream.Result()
	stream.Close()
	if result.StopReason != StopReasonStop || result.Model != "sample-1-2.3" || calls != 1 || catalogCalls != 1 {
		t.Fatal("runtime result identity/calls", result.StopReason, result.Model, calls, catalogCalls)
	}
}
