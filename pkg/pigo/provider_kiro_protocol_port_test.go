package pigo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Scenario-level port from the pinned community suite; fixtures and assertions
// are independently authored for this stricter Go boundary. No upstream code,
// credentials, network clients, or executable tool handlers are used.
func TestKiroPortProtocolRepeatedDeltasRemainDistinct(t *testing.T) {
	wire := kiroOfflineJoin(
		kiroOfflineEvent("assistantResponseEvent", `{"content":"叮"}`),
		kiroOfflineEvent("assistantResponseEvent", `{"content":"叮"}`),
		kiroOfflineEvent("assistantResponseEvent", `{"content":"!"}`),
		kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN","tokenUsage":{"uncachedInputTokens":9,"outputTokens":3,"totalTokens":12,"contextUsagePercentage":17.25}}`),
	)
	got, err := collectKiroOfflineResponse(context.Background(), kiroFragmentReader{bytes.NewReader(wire)}, kiroOfflineTestLimits())
	if err != nil || got.StopReason != StopReasonStop || len(got.Parts) != 3 {
		t.Fatalf("repeated delta collection: %#v %v", got, err)
	}
	var text strings.Builder
	for _, part := range got.Parts {
		if part.Kind != "text" || part.Tool != nil {
			t.Fatalf("unexpected part: %#v", part)
		}
		text.WriteString(part.Text)
	}
	if text.String() != "叮叮!" || got.Usage.TokenCounts["uncachedInputTokens"] != 9 ||
		got.Usage.TokenCounts["outputTokens"] != 3 || got.Usage.TokenCounts["totalTokens"] != 12 ||
		got.Usage.ContextUsagePercentage == nil || *got.Usage.ContextUsagePercentage != 17.25 {
		t.Fatalf("duplicate text or terminal accounting changed: %#v", got)
	}
}

func TestKiroPortProtocolExplicitTerminalMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		want         StopReason
		fail         bool
	}{
		{"empty complete", "END_TURN", StopReasonStop, false},
		{"empty truncated", "MAX_TOKENS", StopReasonLength, false},
		{"refusal", "CONTENT_FILTERED", "", true},
		{"context overflow", "MODEL_CONTEXT_WINDOW_EXCEEDED", "", true},
		{"pause", "PAUSE_TURN", "", true},
		{"future", "FUTURE_STOP", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]string{"stopReason": tc.reason})
			if err != nil {
				t.Fatal(err)
			}
			got, err := collectKiroOfflineResponse(context.Background(),
				bytes.NewReader(kiroOfflineEvent("metadataEvent", string(payload))), kiroOfflineTestLimits())
			if tc.fail {
				if err == nil || !reflect.DeepEqual(got, kiroOfflineResponse{}) {
					t.Fatalf("terminal refusal released result: %#v %v", got, err)
				}
			} else if err != nil || got.StopReason != tc.want || len(got.Parts) != 0 {
				t.Fatalf("explicit empty terminal: %#v %v", got, err)
			}
		})
	}
}

func TestKiroPortToolFragmentsPreserveNestedAndEscapedJSON(t *testing.T) {
	// Split at every ASCII byte boundary, including inside JSON escape sequences.
	// The Unicode escape is intentionally raw: no float/map roundtrip may rewrite it.
	raw := `{"note":"brace } and quote \" and \\ slash","nested":{"n":9007199254740997,"u":"\u4e2d"}}`
	for split := 1; split < len(raw); split++ {
		first, _ := json.Marshal(map[string]string{"input": raw[:split]})
		second, _ := json.Marshal(map[string]string{"input": raw[split:]})
		wire := kiroOfflineJoin(
			kiroOfflineEvent("toolUseEvent", `{"name":"inspect","toolUseId":"port-tool-9","input":{}}`),
			kiroOfflineEvent("toolUseEvent", string(first)),
			kiroOfflineEvent("toolUseEvent", string(second)),
			kiroOfflineEvent("toolUseEvent", `{"stop":true}`),
			kiroOfflineEvent("metadataEvent", `{"stopReason":"TOOL_USE"}`),
		)
		got, err := collectKiroOfflineResponse(context.Background(), kiroFragmentReader{bytes.NewReader(wire)}, kiroOfflineTestLimits())
		if err != nil || got.StopReason != StopReasonToolUse || len(got.Parts) != 1 || got.Parts[0].Kind != "tool" || got.Parts[0].Tool == nil {
			t.Fatalf("split %d rejected: %#v %v", split, got, err)
		}
		tool := got.Parts[0].Tool
		if tool.ID != "port-tool-9" || tool.Name != "inspect" || string(tool.Arguments) != raw {
			t.Fatalf("split %d altered tool evidence: %#v", split, tool)
		}
	}
}

func TestKiroPortExceptionVocabularyNeverLeaksOrReplays(t *testing.T) {
	for _, tc := range []struct {
		token string
		class kiroFailureClass
	}{
		{"error", kiroFailureUnavailable}, {"InternalServerException", kiroFailureUnavailable},
		{"validationError", kiroFailureValidation}, {"ValidationException", kiroFailureValidation},
		{"throttlingError", kiroFailureThrottle}, {"ThrottlingException", kiroFailureThrottle},
		{"serviceUnavailableError", kiroFailureUnavailable}, {"ServiceUnavailableException", kiroFailureUnavailable},
		{"constructor", kiroFailureUnknown}, {"FutureException", kiroFailureUnknown},
	} {
		t.Run(tc.token, func(t *testing.T) {
			wire := kiroOfflineJoin(
				kiroOfflineEvent("assistantResponseEvent", `{"content":"staged private response"}`),
				kiroOfflineFrame("exception", tc.token, `{"message":"PRIVATE_FIXTURE_DO_NOT_EXPOSE","retryAfterMilliseconds":1900}`),
			)
			got, err := collectKiroOfflineResponse(context.Background(), bytes.NewReader(wire), kiroOfflineTestLimits())
			var exception *kiroOfflineException
			if tc.class == kiroFailureUnknown {
				if err == nil || errors.As(err, &exception) || !reflect.DeepEqual(got, kiroOfflineResponse{}) || strings.Contains(err.Error(), "PRIVATE_FIXTURE") || strings.Contains(err.Error(), "1900") {
					t.Fatalf("unknown exception was promoted or leaked: %#v %v", got, err)
				}
				return
			}
			if !errors.As(err, &exception) || exception.Name != tc.token || !reflect.DeepEqual(got, kiroOfflineResponse{}) ||
				strings.Contains(err.Error(), "PRIVATE_FIXTURE") || strings.Contains(err.Error(), "1900") {
				t.Fatalf("exception lost identity or exposed body: %#v %v", got, err)
			}
			decision := classifyKiroFailure(kiroFailureEvidence{Exception: exception.Name, Dispatch: kiroDispatchSent},
				kiroRetryPolicy{AllowUnsentRetry: true, RemainingAttempts: 5})
			if decision.Class != tc.class || decision.RetryUnsent {
				t.Fatalf("unsafe classification: %#v", decision)
			}
		})
	}
}

func TestKiroPortTextDialectsStayDataAndUnknownShapesFailClosed(t *testing.T) {
	for _, prose := range []string{
		`<tool_use>{"name":"inspect","input":{"key":"sample"}}</tool_use>`,
		`<tool_call>{"name":"inspect","arguments":{}}</tool_call>`,
		"\x60\x60\x60\n<function_call>{\"name\":\"inspect\",\"arguments\":{}}</function_call>\n\x60\x60\x60",
	} {
		payload, _ := json.Marshal(map[string]string{"content": prose})
		got, err := collectKiroOfflineResponse(context.Background(), bytes.NewReader(kiroOfflineJoin(
			kiroOfflineEvent("assistantResponseEvent", string(payload)),
			kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`),
		)), kiroOfflineTestLimits())
		if err != nil || len(got.Parts) != 1 || got.Parts[0].Kind != "text" || got.Parts[0].Text != prose || got.Parts[0].Tool != nil {
			t.Fatalf("prose promoted to executable tool: %#v %v", got, err)
		}
	}
	for _, tc := range []struct{ event, payload string }{
		{"futureEvent", `{"content":"looks like text"}`},
		{"futureEvent", `{"tokenUsage":{"outputTokens":4}}`},
		{"meteringEvent", `{"usage":2.25,"unit":"credit"}`},
		{"reasoningContentEvent", `{"redactedContent":"opaque-data"}`},
	} {
		got, err := collectKiroOfflineResponse(context.Background(), bytes.NewReader(kiroOfflineJoin(
			kiroOfflineEvent(tc.event, tc.payload),
			kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`),
		)), kiroOfflineTestLimits())
		if err == nil || !reflect.DeepEqual(got, kiroOfflineResponse{}) {
			t.Fatalf("unsupported shape silently accepted: %#v %v", got, err)
		}
	}
}

func TestKiroPortMalformedRequestIsNotContextOverflow(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   kiroFailureClass
	}{
		{"REQUEST_BODY_INVALID", kiroFailureValidation},
		{"CONTENT_LENGTH_EXCEEDS_THRESHOLD", kiroFailureInputSize},
		{"MONTHLY_REQUEST_COUNT", kiroFailureQuota},
	} {
		got := classifyKiroFailure(kiroFailureEvidence{Status: 400, Reason: tc.reason, Dispatch: kiroDispatchSent},
			kiroRetryPolicy{AllowUnsentRetry: true, RemainingAttempts: 3})
		if got.Class != tc.want || got.RetryUnsent {
			t.Fatalf("reason %s misclassified: %#v", tc.reason, got)
		}
	}
}

// The community event helper omits :content-type. This is an intentional
// compatibility difference, not a license to silently alter copied fixtures.
func TestKiroPortOrdinaryEventRequiresContentType(t *testing.T) {
	message := kiroTestEnvelopeMessage("event", "assistantResponseEvent", []byte(`{"content":"synthetic"}`))
	if _, err := classifyKiroEnvelope(message); err != nil {
		t.Fatal(err)
	}
	delete(message.Headers, ":content-type")
	if _, err := classifyKiroEnvelope(message); !errors.Is(err, errKiroEnvelope) {
		t.Fatal("missing ordinary event content type accepted", err)
	}
}
