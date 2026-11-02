package pigo

import "testing"

func TestKiroFailureExactClassification(t *testing.T) {
	for _, tc := range []struct {
		e    kiroFailureEvidence
		want kiroFailureClass
	}{
		{kiroFailureEvidence{Reason: "MONTHLY_REQUEST_COUNT"}, kiroFailureQuota},
		{kiroFailureEvidence{Reason: "CONTENT_LENGTH_EXCEEDS_THRESHOLD"}, kiroFailureInputSize},
		{kiroFailureEvidence{Reason: "REQUEST_BODY_INVALID"}, kiroFailureValidation},
		{kiroFailureEvidence{Reason: "INSUFFICIENT_MODEL_CAPACITY", Status: 403}, kiroFailureCapacity},
		{kiroFailureEvidence{Reason: "USER_REQUEST_RATE_EXCEEDED"}, kiroFailureThrottle},
		{kiroFailureEvidence{Status: 403}, kiroFailureAuth},
		{kiroFailureEvidence{Status: 429}, kiroFailureThrottle},
		{kiroFailureEvidence{Status: 503}, kiroFailureUnavailable},
		{kiroFailureEvidence{Status: 400}, kiroFailureValidation},
		{kiroFailureEvidence{Exception: "ValidationException"}, kiroFailureValidation},
		{kiroFailureEvidence{Exception: "validationError"}, kiroFailureValidation},
		{kiroFailureEvidence{Exception: "ThrottlingException"}, kiroFailureThrottle},
		{kiroFailureEvidence{Exception: "throttlingError"}, kiroFailureThrottle},
		{kiroFailureEvidence{Exception: "ServiceUnavailableException"}, kiroFailureUnavailable},
		{kiroFailureEvidence{Exception: "serviceUnavailableError"}, kiroFailureUnavailable},
		{kiroFailureEvidence{Exception: "InternalServerException"}, kiroFailureUnavailable},
		{kiroFailureEvidence{Exception: "error"}, kiroFailureUnavailable},
		{kiroFailureEvidence{Status: 429, Reason: "new_reason"}, kiroFailureUnknown},
		{kiroFailureEvidence{Status: 503, Exception: "new_exception"}, kiroFailureUnknown},
		{kiroFailureEvidence{Reason: "prefix MONTHLY_REQUEST_COUNT"}, kiroFailureUnknown},
		{kiroFailureEvidence{Reason: "Input is too long"}, kiroFailureUnknown},
		{kiroFailureEvidence{Reason: "monthly_request_count"}, kiroFailureUnknown},
		{kiroFailureEvidence{Status: 200, Reason: "MONTHLY_REQUEST_COUNT"}, kiroFailureUnknown},
		{kiroFailureEvidence{Status: 200, Exception: "ThrottlingException"}, kiroFailureUnknown},
		{kiroFailureEvidence{Cancelled: true, Reason: "INSUFFICIENT_MODEL_CAPACITY"}, kiroFailureCancelled},
		{kiroFailureEvidence{ProtocolFailed: true, Status: 503}, kiroFailureProtocol},
	} {
		got := classifyKiroFailure(tc.e, kiroRetryPolicy{AllowUnsentRetry: true, RemainingAttempts: 3})
		if got.Class != tc.want || got.RetryUnsent {
			t.Fatalf("classification: got %+v want %s", got, tc.want)
		}
	}
}

func TestKiroFailureUnsentRetryRequiresEveryGuard(t *testing.T) {
	good := kiroFailureEvidence{TransportFailed: true, Dispatch: kiroDispatchNotSent}
	policy := kiroRetryPolicy{AllowUnsentRetry: true, RemainingAttempts: 1}
	if got := classifyKiroFailure(good, policy); got.Class != kiroFailureTransport || !got.RetryUnsent {
		t.Fatalf("proven unsent transport: %+v", got)
	}
	for _, mutate := range []func(*kiroFailureEvidence){
		func(e *kiroFailureEvidence) { e.Dispatch = kiroDispatchUnknown },
		func(e *kiroFailureEvidence) { e.Dispatch = kiroDispatchSent },
		func(e *kiroFailureEvidence) { e.Dispatch = kiroDispatchEvidence(255) },
		func(e *kiroFailureEvidence) { e.OutputObserved = true },
		func(e *kiroFailureEvidence) { e.ToolExecuted = true },
		func(e *kiroFailureEvidence) { e.Cancelled = true },
		func(e *kiroFailureEvidence) { e.ProtocolFailed = true },
		func(e *kiroFailureEvidence) { e.Status = 503 },
		func(e *kiroFailureEvidence) { e.Reason = "INSUFFICIENT_MODEL_CAPACITY" },
		func(e *kiroFailureEvidence) { e.Exception = "ThrottlingException" },
	} {
		e := good
		mutate(&e)
		if got := classifyKiroFailure(e, policy); got.RetryUnsent {
			t.Fatalf("unsafe retry: %+v", got)
		}
	}
	for _, p := range []kiroRetryPolicy{{}, {AllowUnsentRetry: true}, {AllowUnsentRetry: true, RemainingAttempts: -1}, {RemainingAttempts: 1}} {
		if got := classifyKiroFailure(good, p); got.RetryUnsent {
			t.Fatalf("policy bypass: %+v", got)
		}
	}
}
