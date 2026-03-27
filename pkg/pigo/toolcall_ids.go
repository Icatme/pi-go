package pigo

import "strings"

func NormalizeSimpleToolCallID(id string) string {
	var builder strings.Builder
	builder.Grow(len(id))

	for _, ch := range id {
		if ('a' <= ch && ch <= 'z') || ('A' <= ch && ch <= 'Z') || ('0' <= ch && ch <= '9') || ch == '_' || ch == '-' {
			builder.WriteRune(ch)
			continue
		}
		builder.WriteByte('_')
	}

	normalized := builder.String()
	if len(normalized) > 64 {
		normalized = normalized[:64]
	}
	return strings.TrimRight(normalized, "_")
}

func NormalizeOpenAIResponsesToolCallID(id string, targetModel Model, source AssistantMessage) string {
	if targetModel.Provider != "openai-codex" {
		return NormalizeSimpleToolCallID(id)
	}

	callID, itemID, found := strings.Cut(id, "|")
	if !found {
		return NormalizeSimpleToolCallID(id)
	}

	normalizedCallID := NormalizeSimpleToolCallID(callID)
	isForeignToolCall := source.Provider != targetModel.Provider || source.API != targetModel.API

	var normalizedItemID string
	if isForeignToolCall {
		normalizedItemID = "fc_" + ShortHash(itemID)
	} else {
		normalizedItemID = NormalizeSimpleToolCallID(itemID)
		if !strings.HasPrefix(normalizedItemID, "fc_") {
			normalizedItemID = NormalizeSimpleToolCallID("fc_" + normalizedItemID)
		}
	}

	if len(normalizedItemID) > 64 {
		normalizedItemID = normalizedItemID[:64]
	}

	return normalizedCallID + "|" + normalizedItemID
}
