package pigo

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var errKiroToolMessage = errors.New("kiro: unsupported completed tool projection")

// projectKiroCompletedTools consumes whole-response evidence from the collector,
// AFTER the HTTP boundary has also validated EOF, length and Close. It is pure:
// no tools are executed, no provisional tool deltas are emitted. allowedNames
// must be the request's explicit tool-name set; this is NOT schema validation or
// agent execution approval. Inputs must not mutate concurrently.
func projectKiroCompletedTools(response kiroOfflineResponse, model Model, timestamp time.Time, allowedNames []string, maxBytes, maxTools int) (AssistantMessage, error) {
	zero := AssistantMessage{}
	response, diagnostics, maxBytes, observationErr := splitKiroContextObservations(response, timestamp, maxBytes)
	if observationErr != nil {
		return zero, errKiroToolMessage
	}
	if !kiroTextModelIdentityValid(model) || maxBytes < 2 || maxBytes > 1<<20 || maxTools < 1 || maxTools > 128 || len(allowedNames) < 1 || len(allowedNames) > 128 || len(response.Parts) < 1 || len(response.Parts) > 4096 || response.StopReason != StopReasonToolUse || response.Usage.StopReason != "TOOL_USE" {
		return zero, errKiroToolMessage
	}
	allowed := kiroAllowedToolNames(allowedNames)
	if allowed == nil {
		return zero, errKiroToolMessage
	}
	message := AssistantMessage{API: model.API, Provider: model.Provider, Model: model.ID, Timestamp: timestamp, StopReason: StopReasonToolUse, Diagnostics: diagnostics}
	if !applyKiroCompletedUsage(&message, response.Usage) {
		return zero, errKiroToolMessage
	}
	remaining := maxBytes
	seen := make(map[string]bool)
	for _, part := range response.Parts {
		if part.Signature != "" || part.ContextUsagePercentage != nil {
			return zero, errKiroToolMessage
		}
		switch part.Kind {
		case "text":
			if len(seen) != 0 || part.Tool != nil || len(part.Text) > remaining || !utf8.ValidString(part.Text) {
				return zero, errKiroToolMessage
			}
			remaining -= len(part.Text)
			message.Content = append(message.Content, TextContent{Text: part.Text})
		case "tool":
			call := part.Tool
			if part.Text != "" || call == nil || !kiroNativeToolID(call.ID) || seen[call.ID] || !allowed[call.Name] || len(seen) >= maxTools {
				return zero, errKiroToolMessage
			}
			for _, n := range []int{len(call.ID), len(call.Name), len(call.Arguments)} {
				if n > remaining {
					return zero, errKiroToolMessage
				}
				remaining -= n
			}
			if _, err := decodeKiroJSONObject(call.Arguments, maxBytes); err != nil {
				return zero, errKiroToolMessage
			}
			raw := string(call.Arguments)
			decoder := json.NewDecoder(strings.NewReader(raw))
			decoder.UseNumber()
			var args map[string]any
			if decoder.Decode(&args) != nil || !kiroToolArgumentEvidenceMatches(raw, args, maxBytes) {
				return zero, errKiroToolMessage
			}
			seen[call.ID] = true
			message.Content = append(message.Content, ToolCall{ID: call.ID, Name: call.Name, RawArguments: raw, Arguments: args})
		default:
			return zero, errKiroToolMessage
		}
	}
	if len(seen) == 0 {
		return zero, errKiroToolMessage
	}
	return message, nil
}

// kiroAllowedToolNames snapshots the explicit request allowlist before transport.
func kiroAllowedToolNames(names []string) map[string]bool {
	if len(names) < 1 || len(names) > 128 {
		return nil
	}
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		if name == "" || len(name) > 256 || !utf8.ValidString(name) || strings.TrimSpace(name) != name || strings.IndexFunc(name, unicode.IsControl) >= 0 || allowed[name] {
			return nil
		}
		allowed[name] = true
	}
	return allowed
}
