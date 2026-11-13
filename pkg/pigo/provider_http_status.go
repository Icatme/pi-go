package pigo

import (
	"encoding/json"
	"errors"
)

// providerHTTPStatusError is attached only at an actual non-2xx HTTP response.
// It preserves the underlying error and has no effect on retry decisions.
type providerHTTPStatusError struct {
	status int
	cause  error
}

func (e *providerHTTPStatusError) Error() string { return e.cause.Error() }
func (e *providerHTTPStatusError) Unwrap() error { return e.cause }
func withProviderHTTPStatus(err error, status int) error {
	if err == nil || status < 300 || status > 599 {
		return err
	}
	return &providerHTTPStatusError{status: status, cause: err}
}

// ProviderHTTPStatusOf returns a recorded HTTP error status, when available.
// Absence includes older diagnostics, unsupported adapters, transport failures,
// and errors within a successful HTTP stream. A status is not retry permission.
func ProviderHTTPStatusOf(message AssistantMessage) (int, bool) {
	for i := len(message.Diagnostics) - 1; i >= 0; i-- {
		d := message.Diagnostics[i]
		if d.Type != providerFailureDiagnostic || d.Error == nil {
			continue
		}
		var status int
		switch v := d.Details["http_status"].(type) {
		case int:
			status = v
		case float64:
			if v < 300 || v > 599 || v != float64(int(v)) {
				return 0, false
			}
			status = int(v)
		case json.Number:
			n, err := v.Int64()
			if err != nil || n < 300 || n > 599 {
				return 0, false
			}
			status = int(n)
		default:
			return 0, false
		}
		if status < 300 || status > 599 {
			return 0, false
		}
		return status, true
	}
	return 0, false
}

func recordedProviderHTTPStatus(err error) int {
	var value *providerHTTPStatusError
	if errors.As(err, &value) {
		return value.status
	}
	return 0
}
