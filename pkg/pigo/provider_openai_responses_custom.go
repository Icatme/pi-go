package pigo

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Native input is bounded independently of SSE frame and pending-event limits.
// Codemode applies its own smaller source limit after this transport boundary.
const (
	MaxCustomToolInputBytes = 1 << 20
	maxCustomResponseBytes  = 4 << 20
	maxCustomResponseCalls  = 128
)

// Malformed custom output is still output. Do not let model-controlled names
// or statuses containing a retry phrase cause the request to be sent again.
func markOpenAIResponsesCustomOutput(state *openAIResponsesStreamingState, event map[string]any) {
	kind, _ := event["type"].(string)
	if strings.HasPrefix(kind, "response.custom_tool_call_input.") {
		state.CustomOutputSeen = true
	}
	mark := func(item any) {
		if value, ok := item.(map[string]any); ok && value["type"] == "custom_tool_call" {
			state.CustomOutputSeen = true
		}
	}
	mark(event["item"])
	if response, ok := event["response"].(map[string]any); ok {
		if output, ok := response["output"].([]any); ok {
			for _, item := range output {
				mark(item)
			}
		}
	}
}

func processOpenAIResponsesCustomItem(response *AssistantMessage, stream *AssistantMessageEventStream, state *openAIResponsesStreamingState, event map[string]any, item openAIResponsesResponseItem, done bool) error {
	if item.Namespace != "" {
		return fmt.Errorf("OpenAI Responses custom tool namespaces are not enabled")
	}
	property := state.CustomTools[item.Name]
	if property == "" {
		return fmt.Errorf("OpenAI Responses undeclared custom tool %q", item.Name)
	}
	if item.CallID == "" || len(item.CallID) > 128 || !strings.HasPrefix(item.ID, "ctc_") || len(item.ID) > 128 || item.Input == nil {
		return fmt.Errorf("OpenAI Responses custom tool requires call_id, ctc_ item id and string input")
	}
	if done && item.Status != "" && item.Status != "completed" {
		return fmt.Errorf("OpenAI Responses unfinished custom tool %q: status %s", item.Name, item.Status)
	}
	key := openAIResponsesItemKey(item)
	call := state.ToolCalls[key]
	if call != nil && call.Name != item.Name {
		return fmt.Errorf("OpenAI Responses custom tool name changed")
	}
	for otherKey, other := range state.ToolCalls {
		if otherKey != key && (other.ItemID == item.ID || other.CallID == item.CallID) {
			return fmt.Errorf("OpenAI Responses conflicting custom tool identity")
		}
	}
	if event != nil {
		index, ok := event["output_index"].(float64)
		if !ok {
			return fmt.Errorf("OpenAI Responses custom item requires output_index")
		}
		for previousIndex, previousKey := range state.ToolCallOutputKeys {
			if previousKey == key && float64(previousIndex) != index {
				return fmt.Errorf("OpenAI Responses custom item changed output_index")
			}
		}
	}
	if err := registerOpenAIResponsesToolOutputIndex(state, event, item); err != nil {
		return err
	}
	if call == nil {
		if state.CustomCalls >= maxCustomResponseCalls {
			return fmt.Errorf("OpenAI Responses custom tool call limit exceeded")
		}
		if len(*item.Input) > MaxCustomToolInputBytes || len(*item.Input) > maxCustomResponseBytes-state.CustomInputBytes {
			return fmt.Errorf("OpenAI Responses custom input byte limit exceeded")
		}
		if state.ToolCalls == nil {
			state.ToolCalls = make(map[string]*openAIResponsesToolCallState)
		}
		call = &openAIResponsesToolCallState{ContentIndex: len(response.Content), ItemID: item.ID, CallID: item.CallID, Name: item.Name, InputProperty: property}
		state.ToolCalls[key] = call
		state.CustomCalls++
		response.Content = append(response.Content, ToolCall{ID: combineOpenAIResponsesToolCallID(item.CallID, item.ID), Name: item.Name, Arguments: map[string]any{property: ""}})
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventToolCallStart, ContentIndex: call.ContentIndex, Partial: *response})
	}
	if err := appendOpenAIResponsesCustomInput(response, stream, state, call, *item.Input, done); err != nil {
		return err
	}
	if done && !call.Completed {
		call.Completed = true
		block := response.Content[call.ContentIndex].(ToolCall)
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventToolCallEnd, ContentIndex: call.ContentIndex, ToolCall: block, Partial: *response})
		state.FinalizedItemKeys[key] = true
	}
	return nil
}

func validateOpenAIResponsesFunctionItem(state *openAIResponsesStreamingState, item openAIResponsesResponseItem) error {
	if state.CustomTools[item.Name] != "" {
		state.CustomOutputSeen = true
		return fmt.Errorf("OpenAI Responses native custom declaration %q returned a function call", item.Name)
	}
	for _, other := range state.ToolCalls {
		if other.InputProperty != "" && (other.ItemID == item.ID || other.CallID == item.CallID) {
			return fmt.Errorf("OpenAI Responses function call conflicts with custom call identity")
		}
	}
	return nil
}

func processOpenAIResponsesCustomInput(response *AssistantMessage, stream *AssistantMessageEventStream, state *openAIResponsesStreamingState, event map[string]any, kind string) error {
	itemID, idOK := event["item_id"].(string)
	index, indexOK := event["output_index"].(float64)
	if !idOK || itemID == "" || !indexOK {
		return fmt.Errorf("OpenAI Responses custom input requires item_id and output_index")
	}
	call, err := resolveOpenAIResponsesToolCall(state, event)
	if err != nil {
		return err
	}
	if call == nil || call.InputProperty == "" || state.ToolCalls[state.ToolCallOutputKeys[int(index)]] != call {
		return fmt.Errorf("OpenAI Responses custom input targets an unknown or non-custom call")
	}
	field := "delta"
	done := kind == "response.custom_tool_call_input.done"
	if done {
		field = "input"
	}
	input, ok := event[field].(string)
	if !ok {
		return fmt.Errorf("OpenAI Responses custom input %s must be a string", field)
	}
	if !done {
		if call.InputClosed {
			return fmt.Errorf("OpenAI Responses custom input changed after done")
		}
		if len(input) > MaxCustomToolInputBytes-len(call.Input) {
			return fmt.Errorf("OpenAI Responses custom input byte limit exceeded")
		}
		input = call.Input + input
	}
	return appendOpenAIResponsesCustomInput(response, stream, state, call, input, done)
}

// Emit JSON object deltas so every existing Agent consumer keeps the ordinary
// arguments contract, including quotes, newlines, backslashes and Unicode.
func appendOpenAIResponsesCustomInput(response *AssistantMessage, stream *AssistantMessageEventStream, state *openAIResponsesStreamingState, call *openAIResponsesToolCallState, input string, closeInput bool) error {
	if call.InputClosed {
		if closeInput && input == call.Input {
			return nil
		}
		return fmt.Errorf("OpenAI Responses custom input changed after done")
	}
	if !strings.HasPrefix(input, call.Input) {
		return fmt.Errorf("OpenAI Responses custom input changed non-monotonically")
	}
	added := len(input) - len(call.Input)
	if len(input) > MaxCustomToolInputBytes || added > maxCustomResponseBytes-state.CustomInputBytes {
		return fmt.Errorf("OpenAI Responses custom input byte limit exceeded")
	}
	if added == 0 && !closeInput {
		return nil
	}
	delta := ""
	if !call.InputStarted {
		property, _ := json.Marshal(call.InputProperty)
		delta = "{" + string(property) + ":\""
		call.InputStarted = true
	}
	encoded, _ := json.Marshal(input[len(call.Input):])
	delta += string(encoded[1 : len(encoded)-1])
	if closeInput {
		delta += "\"}"
		call.InputClosed = true
	}
	call.Input = input
	state.CustomInputBytes += added
	block := response.Content[call.ContentIndex].(ToolCall)
	block.Arguments = map[string]any{call.InputProperty: input}
	response.Content[call.ContentIndex] = block
	stream.push(AssistantMessageEvent{Type: AssistantMessageEventToolCallDelta, ContentIndex: call.ContentIndex, Delta: delta, Partial: *response})
	return nil
}
