package pigo

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var errKiroTextMessage = errors.New("kiro: unsupported completed text projection")

// projectKiroCompletedText is pure, buffered host-message projection, NOT event
// streaming or provider registration. The caller supplies model identity/time;
// neither is inferred from the response or verified against a live account.
// Unknown/partial evidence is rejected, never silently lost or recomputed.
func projectKiroCompletedText(response kiroOfflineResponse, model Model, timestamp time.Time, maxTextBytes int) (AssistantMessage, error) {
	zero := AssistantMessage{}
	if maxTextBytes < 1 || maxTextBytes > 1<<20 || len(response.Parts) == 0 || len(response.Parts) > 4096 {
		return zero, errKiroTextMessage
	}
	if !kiroTextModelIdentityValid(model) {
		return zero, errKiroTextMessage
	}
	if !((response.StopReason == StopReasonStop && response.Usage.StopReason == "END_TURN") || (response.StopReason == StopReasonLength && response.Usage.StopReason == "MAX_TOKENS")) {
		return zero, errKiroTextMessage
	}
	// Host Usage has no per-field presence. Accept absent counts or complete
	// reported counts only; missing fields must not become known zeroes.
	if response.Usage.ContextUsagePercentage != nil || response.Usage.NormalizedTokenUsage != nil {
		return zero, errKiroTextMessage
	}
	message := AssistantMessage{API: model.API, Provider: model.Provider, Model: model.ID, Timestamp: timestamp, StopReason: response.StopReason}
	counts := response.Usage.TokenCounts
	if len(counts) != 0 {
		if len(counts) != 5 {
			return zero, errKiroTextMessage
		}
		for _, field := range []struct {
			name   string
			target *int
		}{
			{"uncachedInputTokens", &message.Usage.Input},
			{"outputTokens", &message.Usage.Output},
			{"cacheReadInputTokens", &message.Usage.CacheRead},
			{"cacheWriteInputTokens", &message.Usage.CacheWrite},
			{"totalTokens", &message.Usage.TotalTokens},
		} {
			value, exists := counts[field.name]
			if !exists || value < 0 || int64(int(value)) != value {
				return zero, errKiroTextMessage
			}
			*field.target = int(value)
		}
		message.UsageReported = true
	}
	remaining, used := maxTextBytes, 0
	for _, part := range response.Parts {
		if part.Kind != "text" || part.Tool != nil || part.Signature != "" || part.ContextUsagePercentage != nil || len(part.Text) > remaining || !utf8.ValidString(part.Text) {
			return zero, errKiroTextMessage
		}
		remaining -= len(part.Text)
		used += len(part.Text)
		message.Content = append(message.Content, TextContent{Text: part.Text})
	}
	if used == 0 {
		return zero, errKiroTextMessage
	}
	// Cost remains unknown (zero-valued), never calculated from guessed model
	// rates, context percentages or credits. Zero does not mean free service.
	return message, nil
}

func kiroTextModelIdentityValid(model Model) bool {
	for _, field := range []struct {
		value string
		max   int
	}{
		{string(model.API), 128}, {string(model.Provider), 128}, {model.ID, 256},
	} {
		if field.value == "" || len(field.value) > field.max || !utf8.ValidString(field.value) || strings.TrimSpace(field.value) != field.value || strings.IndexFunc(field.value, unicode.IsControl) >= 0 {
			return false
		}
	}
	return true
}
