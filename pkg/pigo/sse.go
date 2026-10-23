package pigo

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const defaultMaxSSEEventBytes = 16 << 20

var ErrSSEEventTooLarge = errors.New("provider SSE event exceeds limit")

func readSSEStream(reader io.Reader, onEvent func(eventName string, data string) (bool, error)) error {
	return readSSEStreamLimit(reader, onEvent, defaultMaxSSEEventBytes)
}

func readSSEStreamLimit(reader io.Reader, onEvent func(string, string) (bool, error), maxBytes int) error {
	if maxBytes <= 0 {
		maxBytes = defaultMaxSSEEventBytes
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, min(64*1024, maxBytes)), maxBytes)
	var eventName string
	var data strings.Builder
	hasData := false
	eventBytes := 0
	dispatch := func() (bool, error) {
		if !hasData {
			eventName = ""
			eventBytes = 0
			return false, nil
		}
		stop, err := onEvent(eventName, data.String())
		// Only callback failures are parsing failures. Reader failures below
		// are interrupted delivery; explicit upstream events keep their kind.
		if err != nil {
			kind, reason := ProviderFailureParse, "event_decode"
			var syntax *json.SyntaxError
			var fieldType *json.UnmarshalTypeError
			switch {
			case isProviderStreamEventCallbackError(err):
				kind, reason = ProviderFailureSDK, "event_callback"
			case errors.As(err, &syntax):
				reason = "invalid_json"
			case errors.As(err, &fieldType):
				reason = "field_type"
			}
			err = withProviderFailure(err, kind, reason)
		}
		eventName = ""
		data.Reset()
		hasData = false
		eventBytes = 0
		return stop, err
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			stop, err := dispatch()
			if err != nil || stop {
				return err
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			value := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			// Count a separator even for empty data lines; they must not bypass the bound.
			if len(value) >= maxBytes-eventBytes {
				return withProviderFailure(ErrSSEEventTooLarge, ProviderFailureParse, "sse_event_too_large")
			}
			eventBytes += len(value) + 1
			if hasData {
				data.WriteByte('\n')
			}
			data.WriteString(value)
			hasData = true
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return withProviderFailure(ErrSSEEventTooLarge, ProviderFailureParse, "sse_event_too_large")
		}
		return withProviderFailure(err, ProviderFailureStream, "body_read")
	}
	_, err := dispatch()
	return err
}
