package pigo

import (
	"bytes"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

var errKiroEnvelope = errors.New("kiro event stream: invalid or unsupported event envelope")

// kiroEnvelope is a classified envelope, NOT a validated application event.
// Payload stays raw: duplicate keys, field schemas, numeric ranges, tool identity
// and lifecycle must be checked by a future event consumer before any output.
// This type has no conversion to pi-go output or executable tool calls.
type kiroEnvelope struct {
	Kind    string
	Name    string
	Payload json.RawMessage
}

// classifyKiroEnvelope intentionally rejects unknown vocabulary instead of
// guessing semantics from payload fields. Names are community-observed, not an
// official supported-service contract. It must receive a CRC-verified frame.
func classifyKiroEnvelope(message kiroEventStreamMessage) (kiroEnvelope, error) {
	stringHeader := func(name string) (string, bool) {
		h, ok := message.Headers[name]
		v, stringValue := h.Value.(string)
		return v, ok && h.Type == 7 && stringValue && v != ""
	}
	kind, ok := stringHeader(":message-type")
	if !ok {
		return kiroEnvelope{}, errKiroEnvelope
	}
	contentType, ok := stringHeader(":content-type")
	if !ok || contentType != "application/json" {
		return kiroEnvelope{}, errKiroEnvelope
	}
	var name string
	switch kind {
	case "event":
		name, ok = stringHeader(":event-type")
		if _, conflict := message.Headers[":exception-type"]; conflict {
			ok = false
		}
		if !ok || !isKiroKnownEventName(name) {
			return kiroEnvelope{}, errKiroEnvelope
		}
	case "exception":
		name, ok = stringHeader(":exception-type")
		if _, conflict := message.Headers[":event-type"]; conflict {
			ok = false
		}
		if !ok || !isKiroKnownExceptionName(name) {
			return kiroEnvelope{}, errKiroEnvelope
		}
	default:
		// Includes generic AWS error frames, which must terminate rather than
		// be treated as successful events. Do not echo untrusted error bodies.
		return kiroEnvelope{}, errKiroEnvelope
	}
	// Validate before trimming: Unicode whitespace such as NBSP is not JSON
	// whitespace and must not make an invalid wire payload appear valid.
	if !utf8.Valid(message.Payload) || !json.Valid(message.Payload) {
		return kiroEnvelope{}, errKiroEnvelope
	}
	payload := bytes.TrimSpace(message.Payload)
	if len(payload) < 2 || payload[0] != '{' {
		return kiroEnvelope{}, errKiroEnvelope
	}
	// Error members sometimes arrive with event framing. Normalize them so a
	// future consumer cannot mistake a known error for successful content.
	if isKiroKnownExceptionName(name) {
		kind = "exception"
	}
	return kiroEnvelope{Kind: kind, Name: name, Payload: payload}, nil
}

func isKiroKnownEventName(name string) bool {
	switch name {
	case "assistantResponseEvent", "codeReferenceEvent", "contextUsageEvent",
		"documentCitationEvent", "metadataEvent", "meteringEvent",
		"reasoningContentEvent", "toolResultEvent", "toolUseEvent":
		return true
	default:
		return isKiroKnownExceptionName(name)
	}
}

func isKiroKnownExceptionName(name string) bool {
	switch name {
	case "error", "throttlingError", "validationError", "serviceUnavailableError",
		"InternalServerException", "ThrottlingException", "ValidationException", "ServiceUnavailableException":
		return true
	default:
		return false
	}
}
