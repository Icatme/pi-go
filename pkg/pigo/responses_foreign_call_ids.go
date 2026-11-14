package pigo

import "fmt"

// uniqueForeignResponsesCallIDs repairs only fully paired foreign history for
// stateless Responses replay. It never changes native Responses identities.
func uniqueForeignResponsesCallIDs(model Model, messages []Message, previousResponseID string) ([]Message, error) {
	counts := map[string]int{}
	foreign := map[string]bool{}
	reserved := map[string]bool{}
	for _, msg := range messages {
		if r, ok := msg.(ToolResultMessage); ok {
			id, _ := splitToolCallID(r.ToolCallID)
			reserved[NormalizeSimpleToolCallID(id)] = true
		}
		a, ok := msg.(AssistantMessage)
		if !ok {
			continue
		}
		for _, b := range a.Content {
			c, ok := b.(ToolCall)
			if !ok {
				continue
			}
			id, _ := splitToolCallID(NormalizeOpenAIResponsesToolCallID(c.ID, model, a))
			counts[id]++
			reserved[id] = true
			if isOpenAIResponsesAPI(a.API) {
				foreign[id] = false
			} else if counts[id] == 1 {
				foreign[id] = true
			}
		}
	}
	repair := map[string]bool{}
	for id, n := range counts {
		if n > 1 && foreign[id] {
			repair[id] = true
		}
	}
	if len(repair) == 0 {
		return messages, nil
	}
	if previousResponseID != "" {
		return nil, fmt.Errorf("OpenAI Responses repeated foreign tool IDs require full history without previous_response_id")
	}
	result := cloneMessages(messages)
	seen := map[string]bool{}
	serial := 0
	for i, msg := range messages {
		a, ok := msg.(AssistantMessage)
		if !ok {
			continue
		}
		next := result[i].(AssistantMessage)
		pending := map[string]int{}
		names := map[string]string{}
		renamed := map[string]string{}
		affected := false
		roundIDs := map[string]bool{}
		for j, b := range a.Content {
			c, ok := b.(ToolCall)
			if !ok {
				continue
			}
			id, _ := splitToolCallID(NormalizeOpenAIResponsesToolCallID(c.ID, model, a))
			if !repair[id] {
				continue
			}
			affected = true
			if a.StopReason != StopReasonToolUse || pending[c.ID] > 0 || roundIDs[id] {
				return nil, fmt.Errorf("OpenAI Responses cannot pair repeated foreign tool IDs")
			}
			roundIDs[id] = true
			pending[c.ID]++
			names[c.ID] = c.Name
			if seen[id] {
				var replacement string
				for {
					serial++
					replacement = fmt.Sprintf("call_pigo_replay_%d", serial)
					if !reserved[replacement] {
						break
					}
				}
				reserved[replacement] = true
				renamed[c.ID] = replacement
				updated := next.Content[j].(ToolCall)
				updated.ID = replacement
				next.Content[j] = updated
			}
			seen[id] = true
		}
		if !affected {
			continue
		}
		for k := i + 1; k < len(messages); k++ {
			tr, ok := messages[k].(ToolResultMessage)
			if !ok {
				break
			}
			if _, target := pending[tr.ToolCallID]; !target {
				continue
			}
			if pending[tr.ToolCallID] != 1 || tr.ToolName != names[tr.ToolCallID] {
				return nil, fmt.Errorf("OpenAI Responses cannot pair repeated foreign tool IDs")
			}
			pending[tr.ToolCallID] = 0
			if id := renamed[tr.ToolCallID]; id != "" {
				updated := result[k].(ToolResultMessage)
				updated.ToolCallID = id
				result[k] = updated
			}
		}
		for _, n := range pending {
			if n != 0 {
				return nil, fmt.Errorf("OpenAI Responses cannot pair repeated foreign tool IDs")
			}
		}
		result[i] = next
	}
	return result, nil
}
