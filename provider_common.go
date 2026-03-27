package pigo

import (
	"context"
	"encoding/json"
	"errors"
)

func applyRequestError(response *AssistantMessage, err error) {
	if err == nil {
		return
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		response.StopReason = StopReasonAborted
		response.ErrorMessage = err.Error()
		return
	}

	response.StopReason = StopReasonError
	response.ErrorMessage = err.Error()
}

func parseStreamingJSONObject(payload string) map[string]any {
	if payload == "" {
		return map[string]any{}
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return map[string]any{}
	}
	return parsed
}
