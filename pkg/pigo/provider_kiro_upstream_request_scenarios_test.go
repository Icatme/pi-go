package pigo

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// Independently authored scenarios; provenance and intentional differences are
// recorded in docs/kiro-upstream-request-test-mapping.md. No upstream test code
// or real credentials/traffic are copied. All executors below are local fakes.
func kiroMigrationRequestOptions() kiroHTTPRequestOptions {
	return kiroHTTPRequestOptions{
		Region:    "eu-central-1",
		Identity:  kiroRequestIdentity{ConversationID: "migration-conversation-17", ProfileARN: "arn:aws:codewhisperer:eu-central-1:000000000000:profile/LOCAL_ONLY", Origin: "PI_GO_TEST_ONLY", ModelID: "migration-fixture-model"},
		Limits:    kiroRequestLimits{Messages: 20, Tools: 10, PayloadBytes: 4096, WireBytes: 8192},
		UserAgent: "pi-go/upstream-scenario-test", InvocationID: "12345678-1234-1234-1234-123456789abc",
	}
}

func kiroMigrationObject(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		t.Fatal("fixture object shape mismatch")
	}
	return value
}
func kiroMigrationString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal("fixture string field missing")
	}
	return value
}

func TestKiroUpstreamRequestExplicitProfileRegionAndAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name             string
		kind             kiroCredentialKind
		token, tokenType string
	}{
		{"bearer", kiroCredentialBearer, "LOCAL_ONLY_SCENARIO_BEARER", ""},
		{"api_key", kiroCredentialAPIKey, "ksk_LOCAL_ONLY_SCENARIO_KEY", "API_KEY"},
		{"external", kiroCredentialExternalIDP, "LOCAL_ONLY_SCENARIO_EXTERNAL", "EXTERNAL_IDP"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := kiroMigrationRequestOptions()
			calls := 0
			body := &kiroAttemptBody{reader: bytes.NewReader(kiroAttemptWire())}
			result, err := runKiroInjectedAttempt(context.Background(), Context{Messages: []Message{UserMessage{Content: "original fixture question"}}}, nil, options, tc.kind, tc.token, func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "POST" || req.URL.String() != "https://runtime.eu-central-1.kiro.dev/generateAssistantResponse" || req.Header.Get("X-Amz-Target") != "" {
					t.Fatal("runtime request drifted into management operation")
				}
				if req.Header.Get("Authorization") != "Bearer "+tc.token || req.Header.Get("Tokentype") != tc.tokenType || req.Header.Get("Content-Type") != "application/json" || req.Header.Get("Accept") != "application/vnd.amazon.eventstream" {
					t.Fatal("explicit request authentication/header mismatch")
				}
				raw, readErr := io.ReadAll(req.Body)
				if readErr != nil || bytes.Contains(raw, []byte(tc.token)) || strings.Contains(req.URL.String(), tc.token) {
					t.Fatal("fixture credential placed outside header")
				}
				root := kiroMigrationObject(t, raw)
				if len(root) != 3 || kiroMigrationString(t, root["profileArn"]) != "arn:aws:codewhisperer:eu-central-1:000000000000:profile/LOCAL_ONLY" || kiroMigrationString(t, root["agentMode"]) != "vibe" {
					t.Fatal("explicit profile/outer request contract changed")
				}
				state := kiroMigrationObject(t, root["conversationState"])
				if kiroMigrationString(t, state["conversationId"]) != "migration-conversation-17" || kiroMigrationString(t, state["chatTriggerType"]) != "MANUAL" || kiroMigrationString(t, state["agentTaskType"]) != "vibe" {
					t.Fatal("conversation contract changed")
				}
				current := kiroMigrationObject(t, kiroMigrationObject(t, state["currentMessage"])["userInputMessage"])
				if kiroMigrationString(t, current["content"]) != "original fixture question" || kiroMigrationString(t, current["modelId"]) != "migration-fixture-model" || kiroMigrationString(t, current["origin"]) != "PI_GO_TEST_ONLY" {
					t.Fatal("caller content/model/identity rewritten")
				}
				return kiroAttemptResponse(body), nil
			}, kiroAttemptLimits())
			if err != nil || calls != 1 || body.closes != 1 || result.StopReason != StopReasonStop {
				t.Fatal("explicit profile path did not complete one fixture attempt")
			}
		})
	}
}

func TestKiroUpstreamRequestPreflightFailuresNeverInvokeExecutor(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*kiroHTTPRequestOptions)
		kind   kiroCredentialKind
		token  string
	}{
		{"profile_missing", func(o *kiroHTTPRequestOptions) { o.Identity.ProfileARN = "" }, kiroCredentialBearer, "LOCAL_ONLY"},
		{"conversation_missing", func(o *kiroHTTPRequestOptions) { o.Identity.ConversationID = "" }, kiroCredentialBearer, "LOCAL_ONLY"},
		{"mixed_case_region_rejected_not_normalized", func(o *kiroHTTPRequestOptions) { o.Region = "EU-CENTRAL-1" }, kiroCredentialBearer, "LOCAL_ONLY"},
		{"header_injection", func(o *kiroHTTPRequestOptions) { o.UserAgent = "fixture\r\nInjected: yes" }, kiroCredentialBearer, "LOCAL_ONLY"},
		{"bad_api_key", func(*kiroHTTPRequestOptions) {}, kiroCredentialAPIKey, "LOCAL_ONLY_NOT_A_KEY"},
		{"unspecified_kind", func(*kiroHTTPRequestOptions) {}, kiroCredentialKind(""), "LOCAL_ONLY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options := kiroMigrationRequestOptions()
			tc.mutate(&options)
			calls := 0
			result, err := runKiroInjectedAttempt(context.Background(), Context{Messages: []Message{UserMessage{Content: "fixture"}}}, nil, options, tc.kind, tc.token, func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not execute") }, kiroAttemptLimits())
			if err == nil || calls != 0 || !reflect.DeepEqual(result, kiroOfflineResponse{}) {
				t.Fatal("invalid explicit configuration reached execution")
			}
		})
	}
}

func TestKiroUpstreamRequestStableConversationAndCallerSelectedIdentity(t *testing.T) {
	options := kiroMigrationRequestOptions()
	ua, err := kiroCommunityCLIUserAgent("abcdef0123456789abcdef0123456789")
	if err != nil {
		t.Fatal(err)
	}
	options.UserAgent = ua
	options.Identity.Origin = "KIRO_CLI" // Explicit fixture simulation, never a default.
	for _, question := range []string{"first locally authored question", "second locally authored question"} {
		req, err := newKiroOfflineRequestWithToken(context.Background(), Context{Messages: []Message{UserMessage{Content: question}}}, nil, options, kiroCredentialBearer, "LOCAL_ONLY_SESSION_TOKEN")
		if err != nil {
			t.Fatal("session fixture request failed")
		}
		body, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		state := kiroMigrationObject(t, kiroMigrationObject(t, body)["conversationState"])
		if kiroMigrationString(t, state["conversationId"]) != "migration-conversation-17" {
			t.Fatal("explicit session identity regenerated")
		}
		current := kiroMigrationObject(t, kiroMigrationObject(t, state["currentMessage"])["userInputMessage"])
		if kiroMigrationString(t, current["origin"]) != "KIRO_CLI" || kiroMigrationString(t, current["content"]) != question {
			t.Fatal("explicit identity or per-turn content changed")
		}
		if req.Header.Get("User-Agent") != ua || req.Header.Get("X-Amz-User-Agent") != ua || req.Header.Get("Amz-Sdk-Invocation-Id") != "12345678-1234-1234-1234-123456789abc" || req.Header.Get("Amz-Sdk-Request") != "attempt=1; max=1" || req.Header.Get("X-Amzn-Kiro-Agent-Mode") != "vibe" || req.Header.Get("X-Amzn-Codewhisperer-Optout") != "true" {
			t.Fatal("explicit compatibility headers changed")
		}
	}
}

func TestKiroUpstreamRequestUnsignedAudienceDoesNotSelectCredentialKind(t *testing.T) {
	// Upstream infers external kind from aud. This port deliberately requires
	// explicit kind instead; this is a documented difference, not parity.
	encode := base64.RawURLEncoding.EncodeToString
	token := encode([]byte(`{"typ":"JWT"}`)) + "." + encode([]byte(`{"aud":"api://kiro","fixture":"LOCAL_ONLY"}`)) + ".LOCAL_ONLY_SIGNATURE"
	for _, tc := range []struct {
		kind     kiroCredentialKind
		expected string
	}{{kiroCredentialBearer, ""}, {kiroCredentialExternalIDP, "EXTERNAL_IDP"}} {
		headers, err := buildKiroExplicitAuthHeaders(tc.kind, token)
		if err != nil || headers["tokentype"] != tc.expected {
			t.Fatal("unsigned audience changed explicit credential kind")
		}
	}
}

func TestKiroUpstreamRequestHeaderWaitCancellationNeverRetriesOrRefreshes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	result, err := runKiroInjectedAttempt(ctx, Context{Messages: []Message{UserMessage{Content: "fixture"}}}, nil, kiroMigrationRequestOptions(), kiroCredentialBearer, "LOCAL_ONLY_CANCELLATION_TOKEN", func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Context() != ctx {
			t.Fatal("request wait lost caller context")
		}
		cancel()
		return nil, errors.New("LOCAL_ONLY_UNTRUSTED_TRANSPORT_DETAIL")
	}, kiroAttemptLimits())
	if calls != 1 || err == nil || err.Error() != "kiro: injected attempt failed" || !reflect.DeepEqual(result, kiroOfflineResponse{}) {
		t.Fatal("cancelled fixture retried/refreshed or leaked error details")
	}
}
