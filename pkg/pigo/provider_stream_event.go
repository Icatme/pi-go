package pigo

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// observeProviderStreamEvent exposes all JSON fields before an adapter reduces
// an event to its normalized message representation. Protocol framing and
// malformed JSON are handled by the adapter, not passed to the callback.
func observeProviderStreamEvent(data string, model Model, options ProviderStreamOptions) error {
	if options.OnProviderStreamEvent == nil {
		return nil
	}
	data = strings.TrimSpace(data)
	if data == "" || data == "[DONE]" || !json.Valid([]byte(data)) {
		return nil
	}
	// Allocate separate storage so a callback cannot modify parser input or an
	// event retained by another callback invocation.
	if err := options.OnProviderStreamEvent(json.RawMessage([]byte(data)), cloneModel(model)); err != nil {
		return &providerStreamEventCallbackError{err: err}
	}
	return nil
}

type providerStreamEventCallbackError struct {
	err error
}

func (err *providerStreamEventCallbackError) Error() string {
	return fmt.Sprintf("provider stream event callback: %v", err.err)
}

func (err *providerStreamEventCallbackError) Unwrap() error {
	return err.err
}

func isProviderStreamEventCallbackError(err error) bool {
	var callbackError *providerStreamEventCallbackError
	return errors.As(err, &callbackError)
}
