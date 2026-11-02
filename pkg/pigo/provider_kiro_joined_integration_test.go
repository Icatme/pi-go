package pigo

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"
)

// This is an offline component composition test, not a registered-provider or
// Agent tool roundtrip. It never calls http.Client or executes a tool.
func TestKiroJoinedHTTPRequestBlocksAndContextUsage(t *testing.T) {
	conversation, schemas := kiroToolProjectionFixture()
	conversation.Messages[0] = &UserMessage{Content: []ContentBlock{
		TextContent{Text: "find"}, &TextContent{Text: " fixture"},
	}}
	request, err := newKiroOfflineHTTPRequest(context.Background(), conversation, schemas, kiroHTTPRequestFixture())
	if err != nil {
		t.Fatal(err)
	}
	defer request.Body.Close()
	calls := 0
	transport := kiroFixtureRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var envelope kiroRequestEnvelope
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Fatal(err)
		}
		state := envelope.ConversationState
		if len(state.History) != 2 || state.History[0].User == nil ||
			state.History[0].User.Content != "find fixture" ||
			state.History[1].Assistant == nil || len(state.History[1].Assistant.Calls) != 1 {
			t.Fatalf("block history changed: %s", body)
		}
		current := state.CurrentMessage.User
		if string(state.History[1].Assistant.Calls[0].Input) != `{"id":9007199254740993}` ||
			current.Context == nil || len(current.Context.Results) != 1 ||
			current.Context.Results[0].ID != "fixture-1" || current.Context.Results[0].Status != "success" ||
			len(current.Context.Results[0].Content) != 1 || current.Context.Results[0].Content[0].Text != "synthetic result" ||
			state.History[1].Assistant.Calls[0].ID != "fixture-1" || state.History[1].Assistant.Calls[0].Name != "lookup" || current.Content != "" ||
			r.Header.Get("Authorization") != "" {
			t.Fatalf("tool evidence or unauthenticated boundary changed: %s", body)
		}
		wire := kiroOfflineJoin(
			kiroOfflineEvent("contextUsageEvent", `{"contextUsagePercentage":0}`),
			kiroOfflineEvent("assistantResponseEvent", `{"content":"fixture answer"}`),
			kiroOfflineEvent("contextUsageEvent", `{"contextUsagePercentage":42.5}`),
			kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN","tokenUsage":{"outputTokens":2}}`),
		)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(wire)), Request: r}, nil
	})
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	got, err := collectKiroOfflineResponse(request.Context(), response.Body, kiroOfflineTestLimits())
	if err != nil || calls != 1 || got.StopReason != StopReasonStop || len(got.Parts) != 3 {
		t.Fatalf("joined collection: %#v %v", got, err)
	}
	if got.Parts[0].Kind != "context_usage" || got.Parts[0].ContextUsagePercentage == nil ||
		*got.Parts[0].ContextUsagePercentage != 0 || got.Parts[1].Text != "fixture answer" ||
		got.Parts[2].Kind != "context_usage" || got.Parts[2].ContextUsagePercentage == nil ||
		*got.Parts[2].ContextUsagePercentage != 42.5 ||
		!reflect.DeepEqual(got.Usage.TokenCounts, map[string]int64{"outputTokens": 2}) ||
		got.Usage.ContextUsagePercentage != nil || got.Usage.NormalizedTokenUsage != nil {
		t.Fatalf("observations became tokens or final metadata: %#v", got)
	}
}

func TestKiroJoinedHTTPRequestRejectsSignedBlocksAndWireOverflow(t *testing.T) {
	conversation, schemas := kiroToolProjectionFixture()
	conversation.Messages[0] = UserMessage{Content: []ContentBlock{TextContent{Text: "find fixture", TextSignature: "unsupported"}}}
	options := kiroHTTPRequestFixture()
	if request, err := newKiroOfflineHTTPRequest(context.Background(), conversation, schemas, options); err == nil || request != nil {
		if request != nil {
			request.Body.Close()
		}
		t.Fatal("HTTP composition bypassed signed-block rejection")
	}
	conversation.Messages[0] = UserMessage{Content: []ContentBlock{TextContent{Text: "find fixture"}}}
	request, err := newKiroOfflineHTTPRequest(context.Background(), conversation, schemas, options)
	if err != nil {
		t.Fatal(err)
	}
	size := request.ContentLength
	request.Body.Close()
	options.Limits.WireBytes = int(size)
	request, err = newKiroOfflineHTTPRequest(context.Background(), conversation, schemas, options)
	if err != nil {
		t.Fatal("exact serialized block budget rejected", err)
	}
	request.Body.Close()
	options.Limits.WireBytes--
	if request, err := newKiroOfflineHTTPRequest(context.Background(), conversation, schemas, options); err == nil || request != nil {
		if request != nil {
			request.Body.Close()
		}
		t.Fatal("HTTP composition bypassed final serialized budget")
	}
}

func TestKiroJoinedRetryHintNeverAuthorizesDispatchedReplay(t *testing.T) {
	hint, err := parseKiroRetryAfter([]string{"2"}, time.Unix(0, 0), 10*time.Second)
	if err != nil || !hint.Present || hint.Delay != 2*time.Second {
		t.Fatal("fixture hint rejected", err)
	}
	for _, dispatch := range []kiroDispatchEvidence{kiroDispatchUnknown, kiroDispatchSent} {
		decision := classifyKiroFailure(kiroFailureEvidence{
			Status: 429, Dispatch: dispatch,
		}, kiroRetryPolicy{AllowUnsentRetry: true, RemainingAttempts: 2})
		if decision.Class != kiroFailureThrottle || decision.RetryUnsent {
			t.Fatalf("wait hint or permissive policy authorized replay: %#v", decision)
		}
	}
}
