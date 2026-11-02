package pigo

import "errors"

var (
	errKiroCompletionIncomplete = errors.New("kiro: incomplete or inconsistent completion evidence")
	errKiroCompletionCancelled  = errors.New("kiro: response cancelled")
	errKiroCompletionFailed     = errors.New("kiro: response stream failed")
	errKiroCompletionFiltered   = errors.New("kiro: response content filtered")
	errKiroCompletionContext    = errors.New("kiro: response context window exceeded")
	errKiroCompletionPaused     = errors.New("kiro: paused response is unsupported")
)

// kiroCompletionEvidence is supplied by a future response state machine after
// consuming the WHOLE stream. This pure decision function does not verify the
// caller's evidence or classify raw frames. No provider currently calls it.
type kiroCompletionEvidence struct {
	RawStopReason      string
	StreamEnded        bool
	StreamFailed       bool
	Cancelled          bool
	OpenTool           bool
	CompletedToolCount int
}

type kiroCompletionDecision struct {
	Reason       StopReason
	ReleaseTools bool
}

// decideKiroCompletion never retries, continues a paused turn, repairs partial
// arguments or executes tools. ReleaseTools permits passing staged validated
// calls to normal host authorization, not bypassing that authorization.
// The policy is intentionally stricter than the community EOF heuristics.
func decideKiroCompletion(e kiroCompletionEvidence) (kiroCompletionDecision, error) {
	failed := kiroCompletionDecision{Reason: StopReasonError}
	if e.Cancelled {
		return kiroCompletionDecision{Reason: StopReasonAborted}, errKiroCompletionCancelled
	}
	if e.StreamFailed {
		return failed, errKiroCompletionFailed
	}
	if !e.StreamEnded || e.CompletedToolCount < 0 {
		return failed, errKiroCompletionIncomplete
	}
	// Refusal/unsupported terminal states always suppress staged tools, even
	// if otherwise complete calls appeared earlier in the response.
	switch e.RawStopReason {
	case "CONTENT_FILTERED":
		return failed, errKiroCompletionFiltered
	case "MODEL_CONTEXT_WINDOW_EXCEEDED":
		return failed, errKiroCompletionContext
	case "PAUSE_TURN":
		return failed, errKiroCompletionPaused
	}
	if e.OpenTool {
		return failed, errKiroCompletionIncomplete
	}
	switch e.RawStopReason {
	case "END_TURN":
		if e.CompletedToolCount != 0 {
			return failed, errKiroCompletionIncomplete
		}
		return kiroCompletionDecision{Reason: StopReasonStop}, nil
	case "MAX_TOKENS":
		// Length termination never releases tools. A future output builder
		// must discard all staged calls and expose only partial text/reasoning.
		return kiroCompletionDecision{Reason: StopReasonLength}, nil
	case "TOOL_USE":
		if e.CompletedToolCount == 0 {
			return failed, errKiroCompletionIncomplete
		}
		return kiroCompletionDecision{Reason: StopReasonToolUse, ReleaseTools: true}, nil
	default:
		// Missing, lowercase aliases and unknown values are not evidence of
		// success. Context percentage and EOF alone are deliberately absent.
		return failed, errKiroCompletionIncomplete
	}
}
