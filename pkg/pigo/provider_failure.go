package pigo

import (
	"context"
	"errors"
)

// ProviderFailure is diagnostic evidence, never permission to retry. Message is
// untrusted provider/SDK text. Code and Type are also untrusted provider values,
// not retry authorization. These fields can contain private data; do not expose them in
// a public API or an unrestricted log.
type ProviderFailure struct {
	Kind    string
	Reason  string
	Message string
	// Code and Type retain decoded OpenAI Responses error fields when present.
	// Empty values also cover other protocols and older stored diagnostics.
	Code string
	Type string
}

const (
	ProviderFailureUpstream = "upstream_error"
	ProviderFailureStream   = "stream_interrupted"
	ProviderFailureParse    = "response_parse_error"
	ProviderFailureSDK      = "sdk_error"
)

const providerFailureDiagnostic = "provider_failure"

type providerFailureError struct {
	kind, reason    string
	code, errorType string
	cause           error
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

// withProviderErrorDetails preserves protocol fields without interpreting their
// spelling or changing retry decisions. Call only at a decoded error boundary.
func withProviderErrorDetails(err error, code, errorType string) error {
	return &providerFailureError{kind: ProviderFailureUpstream, reason: "error_event", code: code, errorType: errorType, cause: err}
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
		failure.Code, failure.Type = classified.code, classified.errorType
	}
	// A JSON error alone cannot identify the response boundary: local request
	// encoding can wrap the same errors. Protocol readers classify at source.
	details := map[string]any{"kind": failure.Kind, "reason": failure.Reason}
	if failure.Code != "" {
		details["code"] = failure.Code
	}
	if failure.Type != "" {
		details["type"] = failure.Type
	}
	appendAssistantMessageDiagnostic(message, createAssistantMessageDiagnostic(providerFailureDiagnostic, err, details))
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
		code, _ := d.Details["code"].(string)
		errorType, _ := d.Details["type"].(string)
		return ProviderFailure{Kind: kind, Reason: reason, Message: d.Error.Message, Code: code, Type: errorType}, true
	}
	return ProviderFailure{}, false
}
