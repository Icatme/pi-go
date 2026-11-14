package pigo

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

type openAICompletionsContentPart struct {
	text     string
	thinking bool
}

// Compatible providers may use typed content rather than the OpenAI string.
// Decode the entire value before publishing any parts. Unknown/malformed parts
// are errors, not silently dropped output. This does not relax terminal checks.
func decodeOpenAICompletionsContent(raw json.RawMessage) ([]openAICompletionsContentPart, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, err
		}
		return []openAICompletionsContentPart{{text: text}}, nil
	}
	if raw[0] != '[' {
		return nil, errors.New("unsupported completions content shape")
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, errors.New("invalid completions content array")
	}
	var result []openAICompletionsContentPart
	for _, block := range blocks {
		kind, ok := requiredOpenAICompletionsStringField(block, "type")
		if !ok {
			return nil, errors.New("missing completions content type")
		}
		var text string
		switch kind {
		case "text":
			text, ok = requiredOpenAICompletionsStringField(block, "text")
		case "thinking":
			text, ok = decodeOpenAICompletionsNestedText(block["thinking"], "text", true)
		case "reasoning":
			text, ok = decodeOpenAICompletionsNestedText(block["summary"], "summary_text", false)
		default:
			return nil, errors.New("unsupported completions content type")
		}
		if !ok {
			return nil, fmt.Errorf("invalid %s completions content", kind)
		}
		result = append(result, openAICompletionsContentPart{text: text, thinking: kind != "text"})
	}
	return result, nil
}

func decodeOpenAICompletionsNestedText(raw json.RawMessage, kind string, allowString bool) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", false
	}
	if allowString && raw[0] == '"' {
		var s string
		err := json.Unmarshal(raw, &s)
		return s, err == nil
	}
	if raw[0] != '[' {
		return "", false
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return "", false
	}
	var result bytes.Buffer
	for _, block := range blocks {
		typ, ok := requiredOpenAICompletionsStringField(block, "type")
		if !ok || typ != kind {
			return "", false
		}
		text, ok := requiredOpenAICompletionsStringField(block, "text")
		if !ok {
			return "", false
		}
		result.WriteString(text)
	}
	return result.String(), true
}
