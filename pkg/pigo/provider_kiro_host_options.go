package pigo

import (
	"errors"
	"reflect"
)

var errKiroHostOptions = errors.New("kiro: unsupported host options")

// These pure validators are NOT installed by default. A future explicitly
// configured API must attach them before host HTTPClient wrapping. APIKey/Auth
// and custom HTTPClient are deliberately rejected: only its separately injected
// credential source/executor may be used. All unsupported/nonzero (including
// future) option fields fail closed rather than being silently discarded.
func validateKiroHostOptions(_ Model, options ProviderStreamOptions) error {
	if options.RequestContext == nil || options.RequestContext.Err() != nil || !kiroHostTransportAllowed(options.Transport) {
		return errKiroHostOptions
	}
	options.RequestContext = nil
	options.Transport = ""
	if !reflect.ValueOf(options).IsZero() {
		return errKiroHostOptions
	}
	return nil
}

func validateKiroHostSimpleOptions(_ Model, options SimpleStreamOptions) error {
	if options.RequestContext == nil || options.RequestContext.Err() != nil || !kiroHostTransportAllowed(options.Transport) {
		return errKiroHostOptions
	}
	options.RequestContext = nil
	options.Transport = ""
	if !reflect.ValueOf(options).IsZero() {
		return errKiroHostOptions
	}
	return nil
}

// SSE is the host's non-WebSocket streaming selector (also emitted by agent).
// The actual Kiro wire remains AWS binary event-stream, not textual SSE.
// No auto/WebSocket choice or generic client transport override is accepted.
func kiroHostTransportAllowed(transport Transport) bool {
	return transport == "" || transport == TransportSSE
}
