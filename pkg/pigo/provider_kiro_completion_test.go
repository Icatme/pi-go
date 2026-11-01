package pigo

import (
	"errors"
	"testing"
)

func TestKiroCompletionExplicitTerminalPolicies(t *testing.T) {
	for _, tc := range []struct {
		name     string
		evidence kiroCompletionEvidence
		want     kiroCompletionDecision
		err      error
	}{
		{"normal", kiroCompletionEvidence{RawStopReason: "END_TURN", StreamEnded: true}, kiroCompletionDecision{Reason: StopReasonStop}, nil},
		{"tool", kiroCompletionEvidence{RawStopReason: "TOOL_USE", StreamEnded: true, CompletedToolCount: 1}, kiroCompletionDecision{Reason: StopReasonToolUse, ReleaseTools: true}, nil},
		{"length suppresses tools", kiroCompletionEvidence{RawStopReason: "MAX_TOKENS", StreamEnded: true, CompletedToolCount: 1}, kiroCompletionDecision{Reason: StopReasonLength}, nil},
		{"filtered suppresses tools", kiroCompletionEvidence{RawStopReason: "CONTENT_FILTERED", StreamEnded: true, CompletedToolCount: 1}, kiroCompletionDecision{Reason: StopReasonError}, errKiroCompletionFiltered},
		{"context overflow", kiroCompletionEvidence{RawStopReason: "MODEL_CONTEXT_WINDOW_EXCEEDED", StreamEnded: true}, kiroCompletionDecision{Reason: StopReasonError}, errKiroCompletionContext},
		{"pause unsupported", kiroCompletionEvidence{RawStopReason: "PAUSE_TURN", StreamEnded: true}, kiroCompletionDecision{Reason: StopReasonError}, errKiroCompletionPaused},
		{"cancel precedence", kiroCompletionEvidence{RawStopReason: "TOOL_USE", StreamEnded: true, CompletedToolCount: 1, Cancelled: true, StreamFailed: true}, kiroCompletionDecision{Reason: StopReasonAborted}, errKiroCompletionCancelled},
		{"failure precedence", kiroCompletionEvidence{RawStopReason: "TOOL_USE", StreamEnded: true, CompletedToolCount: 1, StreamFailed: true}, kiroCompletionDecision{Reason: StopReasonError}, errKiroCompletionFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decideKiroCompletion(tc.evidence)
			if got != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("got %#v, %v; want %#v, %v", got, err, tc.want, tc.err)
			}
		})
	}
}

func TestKiroCompletionRejectsIncompleteEvidence(t *testing.T) {
	for _, evidence := range []kiroCompletionEvidence{
		{}, {StreamEnded: true},
		{RawStopReason: "end_turn", StreamEnded: true},
		{RawStopReason: "future_reason", StreamEnded: true},
		{RawStopReason: "END_TURN", StreamEnded: true, CompletedToolCount: 1},
		{RawStopReason: "TOOL_USE", StreamEnded: true},
		{RawStopReason: "TOOL_USE", StreamEnded: true, CompletedToolCount: -1},
		{RawStopReason: "TOOL_USE", CompletedToolCount: 1},
		{RawStopReason: "TOOL_USE", StreamEnded: true, CompletedToolCount: 1, OpenTool: true},
		{RawStopReason: "MAX_TOKENS", StreamEnded: true, OpenTool: true},
	} {
		got, err := decideKiroCompletion(evidence)
		if got.ReleaseTools || got.Reason != StopReasonError || !errors.Is(err, errKiroCompletionIncomplete) {
			t.Errorf("accepted incomplete evidence %#v: %#v, %v", evidence, got, err)
		}
	}
}

func FuzzKiroCompletionNeverReleasesOnFailure(f *testing.F) {
	f.Add("TOOL_USE", true, false, false, false, 1)
	f.Add("CONTENT_FILTERED", true, false, false, false, 2)
	f.Fuzz(func(t *testing.T, reason string, ended, failed, cancelled, open bool, count int) {
		e := kiroCompletionEvidence{RawStopReason: reason, StreamEnded: ended, StreamFailed: failed, Cancelled: cancelled, OpenTool: open, CompletedToolCount: count}
		d, err := decideKiroCompletion(e)
		if d.ReleaseTools && (err != nil || reason != "TOOL_USE" || !ended || failed || cancelled || open || count <= 0 || d.Reason != StopReasonToolUse) {
			t.Fatalf("unsafe release for %#v: %#v, %v", e, d, err)
		}
	})
}
