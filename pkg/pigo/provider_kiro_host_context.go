package pigo

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

var errKiroHostContext = errors.New("kiro: unsupported host context")

// prepareKiroHostContext accepts either authoring fields or one initial system
// record, not conflicting representations. It never collapses later changes,
// runs validators/custom marshalers, or authorizes a tool. System role lowering
// still requires the separate explicit policy at request construction.
// Raw schemas are caller-supplied evidence, not recovered from float maps. The
// selected schema must exactly match the ordinary parsed Parameters map.
// Inputs must not mutate concurrently. Output message/parameter values remain
// read-only shared until request serialization; selected raw bytes are copied.
func prepareKiroHostContext(input Context, catalog map[string]json.RawMessage, maxBytes int) (Context, map[string]json.RawMessage, error) {
	bad := func() (Context, map[string]json.RawMessage, error) { return Context{}, nil, errKiroHostContext }
	if maxBytes < 2 || maxBytes > 1<<20 || len(input.Messages) > 4096 || len(input.Tools) > 128 || len(input.HostedTools) != 0 || len(catalog) > 128 {
		return bad()
	}
	out := input
	out.Messages = append([]Message(nil), input.Messages...)
	out.Tools = append([]Tool(nil), input.Tools...)
	for i, message := range input.Messages {
		var system SystemMessage
		switch m := message.(type) {
		case SystemMessage:
			system = m
		case *SystemMessage:
			if m == nil {
				return bad()
			}
			system = *m
		default:
			continue
		}
		if i != 0 || input.SystemPrompt != "" || len(input.Tools) != 0 || len(system.ToolsRemoved) != 0 || len(system.ToolsAdded) > 128 || len(system.Sections) > 128 {
			return bad()
		}
		// Bound material before rendering; include separator overhead conservatively.
		remaining := maxBytes
		consume := func(s string) bool {
			if !utf8.ValidString(s) || len(s) > remaining {
				return false
			}
			remaining -= len(s)
			return true
		}
		if !consume(system.Content) {
			return bad()
		}
		for key, value := range system.Sections {
			if !consume(key) || value == nil || remaining < 2 {
				return bad()
			}
			remaining -= 2
			if !consume(*value) {
				return bad()
			}
		}
		out.SystemPrompt = GetSystemMessageText(system)
		out.Tools = append([]Tool(nil), system.ToolsAdded...)
		out.Messages = append([]Message(nil), input.Messages[1:]...)
	}
	if len(out.SystemPrompt) > maxBytes || !utf8.ValidString(out.SystemPrompt) {
		return bad()
	}
	if len(out.Tools) == 0 {
		return out, nil, nil
	}
	selected := make(map[string]json.RawMessage, len(out.Tools))
	remaining := maxBytes
	for _, tool := range out.Tools {
		if _, duplicate := selected[tool.Name]; duplicate {
			return bad()
		}
		raw, exists := catalog[tool.Name]
		parameters, ok := tool.Parameters.(map[string]any)
		if !exists || !ok || parameters == nil || len(raw) > remaining || !kiroToolArgumentEvidenceMatches(string(raw), parameters, remaining) {
			return bad()
		}
		selected[tool.Name] = append(json.RawMessage(nil), raw...)
		remaining -= len(raw)
	}
	return out, selected, nil
}
