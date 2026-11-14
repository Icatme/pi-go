package pigo

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// Preserve only bounded cache observations, never arbitrary provider payloads.
// Null, pending comparison, and reported reason are distinct; none proves a hit.
func appendAnthropicCacheDiagnostics(message *AssistantMessage, raw json.RawMessage) {
	if len(raw) == 0 || len(raw) > 4096 {
		return
	}
	var value any
	if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return
		}
		reasonRaw, ok := fields["cache_miss_reason"]
		if !ok {
			return
		}
		var reason any
		if !bytes.Equal(bytes.TrimSpace(reasonRaw), []byte("null")) {
			var source map[string]json.RawMessage
			if json.Unmarshal(reasonRaw, &source) != nil || source == nil {
				return
			}
			var kind string
			if json.Unmarshal(source["type"], &kind) != nil || len(kind) == 0 || len(kind) > 128 {
				return
			}
			selected := map[string]any{"type": kind}
			if number, ok := source["cache_missed_input_tokens"]; ok {
				digits := strings.TrimSpace(string(number))
				if len(digits) == 0 || strings.Trim(digits, "0123456789") != "" {
					return
				}
				selected["cache_missed_input_tokens"] = json.Number(digits)
			}
			reason = selected
		}
		value = map[string]any{"cache_miss_reason": reason}
	}
	diagnostic := AssistantMessageDiagnostic{Type: "anthropic_cache_diagnostics", Timestamp: time.Now().UTC(), Details: map[string]any{"diagnostics": value}}
	// A repeated message_start must not grow an unbounded diagnostic list.
	for i := range message.Diagnostics {
		if message.Diagnostics[i].Type == diagnostic.Type {
			message.Diagnostics = append([]AssistantMessageDiagnostic(nil), message.Diagnostics...)
			message.Diagnostics[i] = diagnostic
			return
		}
	}
	message.Diagnostics = append(message.Diagnostics, diagnostic)
}
