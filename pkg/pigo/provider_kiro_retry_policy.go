package pigo

// Pure offline evidence classification. This does not parse response bodies,
// perform I/O, refresh credentials, schedule retries or authorize service use.
type kiroFailureClass string

const (
	kiroFailureUnknown     kiroFailureClass = "unknown"
	kiroFailureCancelled   kiroFailureClass = "cancelled"
	kiroFailureProtocol    kiroFailureClass = "protocol"
	kiroFailureTransport   kiroFailureClass = "transport"
	kiroFailureAuth        kiroFailureClass = "auth"
	kiroFailureQuota       kiroFailureClass = "quota"
	kiroFailureInputSize   kiroFailureClass = "input_size"
	kiroFailureValidation  kiroFailureClass = "validation"
	kiroFailureCapacity    kiroFailureClass = "capacity"
	kiroFailureThrottle    kiroFailureClass = "throttle"
	kiroFailureUnavailable kiroFailureClass = "unavailable"
)

type kiroDispatchEvidence uint8

const (
	kiroDispatchUnknown kiroDispatchEvidence = iota
	kiroDispatchNotSent
	kiroDispatchSent
)

// Status is a non-success HTTP status, or zero for a local/stream failure.
// For exceptions inside a successful HTTP event stream, pass Status=0.
// Reason and Exception must be exact service codes from a separately validated
// bounded parser, never arbitrary message substrings. They are not retained in
// the decision. NotSent requires transport proof: a timeout is not that proof.
type kiroFailureEvidence struct {
	Status          int
	Reason          string
	Exception       string
	Cancelled       bool
	ProtocolFailed  bool
	TransportFailed bool
	Dispatch        kiroDispatchEvidence
	OutputObserved  bool
	ToolExecuted    bool
}

type kiroRetryPolicy struct {
	AllowUnsentRetry  bool
	RemainingAttempts int
}

type kiroFailureDecision struct {
	Class       kiroFailureClass
	RetryUnsent bool
}

// classifyKiroFailure deliberately never authorizes replay after dispatch.
// A transient category is a diagnostic, not proof that replay is safe or free.
// Only an otherwise unambiguous local transport failure with positive NotSent
// evidence may produce RetryUnsent, and only within explicit caller policy.
func classifyKiroFailure(e kiroFailureEvidence, policy kiroRetryPolicy) kiroFailureDecision {
	d := kiroFailureDecision{Class: kiroFailureUnknown}
	if e.Cancelled {
		d.Class = kiroFailureCancelled
		return d
	}
	if e.ProtocolFailed {
		d.Class = kiroFailureProtocol
		return d
	}
	if e.Status != 0 && (e.Status < 400 || e.Status > 599) {
		return d
	}
	// Conflicting transport and service evidence must not become retry advice.
	if e.TransportFailed {
		if e.Status != 0 || e.Reason != "" || e.Exception != "" {
			return d
		}
		d.Class = kiroFailureTransport
		d.RetryUnsent = e.Dispatch == kiroDispatchNotSent &&
			!e.OutputObserved && !e.ToolExecuted &&
			policy.AllowUnsentRetry && policy.RemainingAttempts > 0
		return d
	}
	switch e.Reason {
	case "MONTHLY_REQUEST_COUNT":
		d.Class = kiroFailureQuota
	case "CONTENT_LENGTH_EXCEEDS_THRESHOLD":
		d.Class = kiroFailureInputSize
	case "REQUEST_BODY_INVALID":
		d.Class = kiroFailureValidation
	case "INSUFFICIENT_MODEL_CAPACITY":
		d.Class = kiroFailureCapacity
	case "USER_REQUEST_RATE_EXCEEDED":
		d.Class = kiroFailureThrottle
	case "":
		switch e.Exception {
		case "validationError", "ValidationException":
			d.Class = kiroFailureValidation
		case "throttlingError", "ThrottlingException":
			d.Class = kiroFailureThrottle
		case "serviceUnavailableError", "ServiceUnavailableException", "error", "InternalServerException":
			d.Class = kiroFailureUnavailable
		case "":
			switch e.Status {
			case 401, 403:
				d.Class = kiroFailureAuth
			case 429:
				d.Class = kiroFailureThrottle
			case 500, 502, 503, 504:
				d.Class = kiroFailureUnavailable
			case 400, 422:
				d.Class = kiroFailureValidation
			}
		}
	}
	return d
}
