package pigo

import (
	"encoding/json"
	"fmt"
	"strings"
)

func resolveSamplingParams(model Model, level ModelThinkingLevel, request SamplingParams) SamplingParams {
	level = ModelThinkingLevel(strings.TrimSpace(string(level)))
	if level == "" || level == "none" {
		level = ModelThinkingLevelOff
	}
	effective := ClampThinkingLevel(model, level)
	var result SamplingParams
	for _, layer := range []SamplingParams{model.SamplingParams, model.SamplingParamsByThinkingLevel[effective], request} {
		for key, value := range layer {
			if result == nil {
				result = make(SamplingParams)
			}
			result[key] = cloneAny(value)
		}
	}
	return result
}

// Preserve the typed request for payload hooks and flatten custom sampling
// fields only at the JSON boundary. Raw messages avoid rounding integer fields.
func marshalSamplingParams(request any, params SamplingParams) ([]byte, error) {
	data, err := json.Marshal(request)
	if err != nil || len(params) == 0 {
		return data, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for key, value := range params {
		// Sampling overrides cannot change the protocol/model facts that were
		// already validated or the tool declarations used by the response parser.
		switch key {
		case "model", "messages", "input", "instructions", "tools", "tool_choice",
			"parallel_tool_calls", "response_format", "reasoning", "reasoning_effort",
			"thinking", "enable_thinking", "stream", "stream_options", "store",
			"max_tokens", "max_completion_tokens", "max_output_tokens", "service_tier",
			"text", "include", "prompt_cache_key", "prompt_cache_retention",
			"prompt_cache_options", "metadata", "previous_response_id", "truncation":
			return nil, fmt.Errorf("sampling parameters cannot override protocol field %q", key)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		fields[key] = encoded
	}
	return json.Marshal(fields)
}

func (request openAICompletionsRequest) MarshalJSON() ([]byte, error) {
	if _, overrides := request.SamplingParams["temperature"]; overrides {
		request.Temperature = nil
	}
	type wireRequest openAICompletionsRequest
	return marshalSamplingParams(wireRequest(request), request.SamplingParams)
}

func (request openAIResponsesRequest) MarshalJSON() ([]byte, error) {
	if _, overrides := request.SamplingParams["temperature"]; overrides {
		request.Temperature = nil
	}
	if _, overrides := request.SamplingParams["top_p"]; overrides {
		request.TopP = nil
	}
	type wireRequest openAIResponsesRequest
	return marshalSamplingParams(wireRequest(request), request.SamplingParams)
}
