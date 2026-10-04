package pigo

import (
	"encoding/json"
	"fmt"
)

// These bound the additional native input surface, not the existing top-level
// function declarations or the entire request's memory use.
const (
	maxAnchoredTools     = 256
	maxToolAnchors       = 128
	maxToolAnchorBytes   = 1 << 20
	maxAnchoredToolBytes = 64 << 10
	maxAnchoredToolCalls = 128
)

type openAIResponsesToolPlan struct {
	RequestTools        []Tool
	Anchor              bool
	RequiresFullHistory bool
}

func validateOpenAIResponsesAnchoredFunction(state *openAIResponsesStreamingState, item openAIResponsesResponseItem) error {
	if item.Namespace != "" && item.Namespace != item.Name {
		return fmt.Errorf("OpenAI Responses function call has an unsupported namespace")
	}
	if state.ToolCalls[openAIResponsesItemKey(item)] == nil && len(state.ToolCalls)-state.CustomCalls >= maxAnchoredToolCalls {
		return fmt.Errorf("OpenAI Responses anchored function call limit exceeded")
	}
	for _, previous := range state.ToolCalls {
		if previous.InputProperty != "" {
			continue // custom/function collisions are checked by the shared validator
		}
		if (item.ID != "" && previous.ItemID == item.ID) || (item.CallID != "" && previous.CallID == item.CallID) {
			if previous.ItemID != item.ID || previous.CallID != item.CallID ||
				(previous.Name != "" && (previous.Name != item.Name || previous.Namespace != item.Namespace)) {
				return fmt.Errorf("OpenAI Responses function call changed identity, name or namespace")
			}
		}
	}
	return nil
}

// additional_tools is additive only. A removal or redeclaration must use the
// current tool list, rather than inventing a provider removal/replacement item.
// Read normalized system updates before protocol-specific folding or filtering.
// Resolve once before payload hooks/auth; both request fields consume this plan.
func resolveOpenAIResponsesToolPlan(custom map[string]string, model Model, transcript TranscriptContext) *openAIResponsesToolPlan {
	plan := &openAIResponsesToolPlan{RequestTools: GetCurrentTools(transcript.Messages)}
	if model.API != "openai-responses" {
		return plan
	}
	facts, ok := LookupModelCapabilities(model.Provider, model.ID)
	if !ok || facts.WireAPI != model.API || facts.Capabilities.ToolAdditions != CapabilitySupported {
		return plan
	}
	for index, entry := range transcript.Messages {
		if message, ok := asSystemMessage(entry); ok && index > 0 && (len(message.ToolsAdded) > 0 || len(message.ToolsRemoved) > 0) {
			plan.RequiresFullHistory = true
			break
		}
	}
	// Folding mid-conversation system messages changes the wire representation,
	// not whether inherited server state may contain an obsolete tool set.
	if !supportsTranscriptSystemMessages(model) {
		return plan
	}
	seen := make(map[string]bool)
	anchors, additions, bytes := 0, 0, 0
	for index, entry := range transcript.Messages {
		message, ok := asSystemMessage(entry)
		if !ok {
			continue
		}
		if len(message.ToolsRemoved) > 0 {
			return plan
		}
		if index > 0 && len(message.ToolsAdded) > 0 {
			anchors++
			if anchors > maxToolAnchors {
				return plan
			}
		}
		for _, tool := range message.ToolsAdded {
			if seen[tool.Name] {
				return plan
			}
			seen[tool.Name] = true
			if index == 0 {
				continue
			}
			// This slice documents function additions. Static native custom tools
			// can coexist, but dynamic native custom tools use the N2 path.
			if custom[tool.Name] != "" {
				return plan
			}
			additions++
			if additions > maxAnchoredTools {
				return plan
			}
			encoded, err := json.Marshal(convertOpenAIResponsesTools(nil, []Tool{tool}))
			if err != nil || len(encoded) > maxAnchoredToolBytes || len(encoded) > maxToolAnchorBytes-bytes {
				return plan
			}
			bytes += len(encoded)
		}
	}
	if anchors == 0 {
		return plan
	}
	plan.Anchor = true
	plan.RequestTools = nil
	if initial := GetInitialSystemMessage(transcript.Messages); initial != nil {
		plan.RequestTools = initial.ToolsAdded
	}
	return plan
}
