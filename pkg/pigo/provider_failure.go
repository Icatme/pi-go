package pigo

import (
	"context"
	"errors"
)

// ProviderFailure is diagnostic evidence, never permission to retry. Message is
// untrusted provider/SDK text and can contain private data; do not expose it in
// a public API or an unrestricted log.
type ProviderFailure struct {
	Kind    string
	Reason  string
	Message string
}

const (
	ProviderFailureUpstream = "upstream_error"
	ProviderFailureStream   = "stream_interrupted"
	ProviderFailureParse    = "response_parse_error"
	ProviderFailureSDK      = "sdk_error"
)

const providerFailureDiagnostic = "provider_failure"

type providerFailureError struct {
	kind, reason string
	cause        error
}

func (e *providerFailureError) Error() string { return e.cause.Error() }
func (e *providerFailureError) Unwrap() error { return e.cause }

func withProviderFailure(err error, kind, reason string) error {
	if err == nil {
		return nil
	}
	var existing *providerFailureError
	if errors.As(err, &existing) {
		return err
	}
	return &providerFailureError{kind: kind, reason: reason, cause: err}
}

func recordProviderFailure(message *AssistantMessage, err error) {
	failure := ProviderFailure{Kind: ProviderFailureSDK, Reason: "unclassified", Message: err.Error()}
	var classified *providerFailureError
	switch {
	case errors.Is(err, context.Canceled):
		failure.Kind, failure.Reason = ProviderFailureSDK, "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		failure.Kind, failure.Reason = ProviderFailureSDK, "timeout"
	case errors.As(err, &classified):
		failure.Kind, failure.Reason = classified.kind, classified.reason
	}
	// A JSON error alone cannot identify the response boundary: local request
	// encoding can wrap the same errors. Protocol readers classify at source.
	appendAssistantMessageDiagnostic(message, createAssistantMessageDiagnostic(providerFailureDiagnostic, err,
		map[string]any{"kind": failure.Kind, "reason": failure.Reason}))
}

// ProviderFailureOf returns the final classified failure without interpreting
// error text. The existing message diagnostics survive cloning and JSON storage.
func ProviderFailureOf(message AssistantMessage) (ProviderFailure, bool) {
	for i := len(message.Diagnostics) - 1; i >= 0; i-- {
		d := message.Diagnostics[i]
		if d.Type != providerFailureDiagnostic || d.Error == nil {
			continue
		}
		kind, _ := d.Details["kind"].(string)
		reason, _ := d.Details["reason"].(string)
		return ProviderFailure{Kind: kind, Reason: reason, Message: d.Error.Message}, true
	}
	return ProviderFailure{}, false
}
