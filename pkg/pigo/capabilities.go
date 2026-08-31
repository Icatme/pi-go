package pigo

import "sort"

// CapabilitySupport describes whether pi-go can faithfully accept and dispatch
// a capability for a registered provider/model pair. A fixed, silently ignored,
// or otherwise non-configurable request parameter is unsupported. Unknown must
// be treated as unsupported by fail-closed consumers. These statuses describe
// the local adapter contract, not live upstream availability or authorization.
type CapabilitySupport string

const (
	CapabilityUnknown     CapabilitySupport = "unknown"
	CapabilityUnsupported CapabilitySupport = "unsupported"
	CapabilitySupported   CapabilitySupport = "supported"
)

// ModelCapabilities contains model-contract facts. On APIModule,
// ProviderModule.ModelCapabilities, and Model.Capabilities, empty fields mean
// unspecified and later registry layers may refine them. Lookup snapshots
// normalize every status to unknown, unsupported, or supported. For a
// reasoning model, sampling support is only actionable at the listed reasoning
// levels; a supported sampling field without such a list normalizes to unknown.
type ModelCapabilities struct {
	Streaming                  CapabilitySupport    `json:"streaming"`
	Tools                      CapabilitySupport    `json:"tools"`
	StrictTools                CapabilitySupport    `json:"strict_tools"`
	ToolChoice                 CapabilitySupport    `json:"tool_choice"`
	Reasoning                  CapabilitySupport    `json:"reasoning"`
	ReasoningLevels            CapabilitySupport    `json:"reasoning_levels"`
	SupportedReasoningLevels   []ModelThinkingLevel `json:"supported_reasoning_levels,omitempty"`
	DefaultReasoningLevel      ModelThinkingLevel   `json:"default_reasoning_level,omitempty"`
	Temperature                CapabilitySupport    `json:"temperature"`
	TemperatureReasoningLevels []ModelThinkingLevel `json:"temperature_reasoning_levels,omitempty"`
	TopP                       CapabilitySupport    `json:"top_p"`
	TopPReasoningLevels        []ModelThinkingLevel `json:"top_p_reasoning_levels,omitempty"`
	ParallelToolCalls          CapabilitySupport    `json:"parallel_tool_calls"`
}

// ResponseFormatCapabilities describes the structured-output formats accepted
// by pi-go's public ResponseFormat contract for a concrete model.
type ResponseFormatCapabilities struct {
	JSONObject CapabilitySupport `json:"json_object"`
	JSONSchema CapabilitySupport `json:"json_schema"`
}

// ModelCapabilitySnapshot is an immutable-by-convention copy of the current
// registry facts for one provider/model pair.
type ModelCapabilitySnapshot struct {
	Provider        Provider                   `json:"provider"`
	ModelID         string                     `json:"model_id"`
	WireAPI         API                        `json:"wire_api"`
	BaseURL         string                     `json:"base_url"`
	Input           []InputType                `json:"input,omitempty"`
	ContextWindow   int                        `json:"context_window,omitempty"`
	MaxOutputTokens int                        `json:"max_output_tokens,omitempty"`
	Capabilities    ModelCapabilities          `json:"capabilities"`
	ResponseFormats ResponseFormatCapabilities `json:"response_formats"`
	HostedTools     []HostedToolType           `json:"hosted_tools,omitempty"`
}

// ProviderCapabilitySnapshot is a deterministic, point-in-time copy of one
// provider module's registered model capabilities.
type ProviderCapabilitySnapshot struct {
	Provider Provider                  `json:"provider"`
	Models   []ModelCapabilitySnapshot `json:"models"`
}

// LookupModelCapabilities returns the current capability facts for an exact
// registered provider/model pair. It returns false for an unknown provider or
// model; callers must not fall back to model-name inference.
func LookupModelCapabilities(provider Provider, modelID string) (ModelCapabilitySnapshot, bool) {
	module := resolveProviderModule(provider)
	if module == nil {
		return ModelCapabilitySnapshot{}, false
	}
	model, ok := module.Models[modelID]
	if !ok {
		return ModelCapabilitySnapshot{}, false
	}
	return buildModelCapabilitySnapshot(*module, cloneModel(model)), true
}

// SnapshotProviderModelCapabilities returns a sorted point-in-time snapshot of
// a registered provider. Replacing a dynamic catalog never mutates a previously
// returned snapshot.
func SnapshotProviderModelCapabilities(provider Provider) (ProviderCapabilitySnapshot, bool) {
	module := resolveProviderModule(provider)
	if module == nil {
		return ProviderCapabilitySnapshot{}, false
	}

	modelIDs := make([]string, 0, len(module.Models))
	for modelID := range module.Models {
		modelIDs = append(modelIDs, modelID)
	}
	sort.Strings(modelIDs)

	snapshot := ProviderCapabilitySnapshot{
		Provider: provider,
		Models:   make([]ModelCapabilitySnapshot, 0, len(modelIDs)),
	}
	for _, modelID := range modelIDs {
		snapshot.Models = append(snapshot.Models, buildModelCapabilitySnapshot(*module, cloneModel(module.Models[modelID])))
	}
	return snapshot, true
}

func buildModelCapabilitySnapshot(module ProviderModule, model Model) ModelCapabilitySnapshot {
	apiModule := resolveAPIModule(model.API)
	capabilities := ModelCapabilities{}
	if apiModule != nil {
		capabilities = cloneModelCapabilities(apiModule.Capabilities)
	}
	capabilities = mergeModelCapabilities(capabilities, module.ModelCapabilities)
	capabilities = mergeModelCapabilities(capabilities, model.Capabilities)

	runtimeStreaming := CapabilityUnsupported
	if apiModule != nil && apiModule.Stream != nil {
		runtimeStreaming = CapabilitySupported
	}
	if capabilities.Streaming == "" {
		capabilities.Streaming = runtimeStreaming
	} else if runtimeStreaming != CapabilitySupported {
		capabilities.Streaming = CapabilityUnsupported
	}
	if runtimeStreaming != CapabilitySupported {
		capabilities.Tools = CapabilityUnsupported
		capabilities.StrictTools = CapabilityUnsupported
		capabilities.ToolChoice = CapabilityUnsupported
		capabilities.Reasoning = CapabilityUnsupported
		capabilities.ReasoningLevels = CapabilityUnsupported
		capabilities.SupportedReasoningLevels = nil
		capabilities.DefaultReasoningLevel = ""
		capabilities.Temperature = CapabilityUnsupported
		capabilities.TemperatureReasoningLevels = nil
		capabilities.TopP = CapabilityUnsupported
		capabilities.TopPReasoningLevels = nil
		capabilities.ParallelToolCalls = CapabilityUnsupported
	}

	if capabilities.Reasoning == "" {
		if model.Reasoning {
			capabilities.Reasoning = CapabilitySupported
		} else {
			capabilities.Reasoning = CapabilityUnsupported
		}
	}
	resolveReasoningLevelCapabilities(&capabilities, model)
	resolveSamplingReasoningCapabilities(&capabilities, model)
	normalizeModelCapabilities(&capabilities)

	responseFormats := responseFormatCapabilitiesFromFacts(module.Capabilities, model)
	if runtimeStreaming != CapabilitySupported {
		responseFormats.SupportsJSONOutput = false
		responseFormats.SupportsJSONSchema = false
	}

	return ModelCapabilitySnapshot{
		Provider:        model.Provider,
		ModelID:         model.ID,
		WireAPI:         model.API,
		BaseURL:         model.BaseURL,
		Input:           append([]InputType(nil), model.Input...),
		ContextWindow:   model.ContextWindow,
		MaxOutputTokens: model.MaxTokens,
		Capabilities:    cloneModelCapabilities(capabilities),
		ResponseFormats: ResponseFormatCapabilities{
			JSONObject: capabilitySupport(responseFormats.SupportsJSONOutput),
			JSONSchema: capabilitySupport(responseFormats.SupportsJSONSchema),
		},
		HostedTools: supportedHostedToolTypes(module.Capabilities.HostedTools, model.HostedTools),
	}
}

func resolveSamplingReasoningCapabilities(capabilities *ModelCapabilities, model Model) {
	if !model.Reasoning {
		capabilities.DefaultReasoningLevel = ""
		capabilities.TemperatureReasoningLevels = nil
		capabilities.TopPReasoningLevels = nil
		return
	}

	capabilities.DefaultReasoningLevel = normalizeReasoningLevel(capabilities.DefaultReasoningLevel)
	capabilities.Temperature = normalizeCapabilitySupport(capabilities.Temperature)
	if capabilities.Temperature == CapabilitySupported {
		capabilities.TemperatureReasoningLevels = normalizeReasoningLevels(capabilities.TemperatureReasoningLevels)
		if len(capabilities.TemperatureReasoningLevels) == 0 {
			capabilities.Temperature = CapabilityUnknown
		}
	} else {
		capabilities.TemperatureReasoningLevels = nil
	}

	capabilities.TopP = normalizeCapabilitySupport(capabilities.TopP)
	if capabilities.TopP == CapabilitySupported {
		capabilities.TopPReasoningLevels = normalizeReasoningLevels(capabilities.TopPReasoningLevels)
		if len(capabilities.TopPReasoningLevels) == 0 {
			capabilities.TopP = CapabilityUnknown
		}
	} else {
		capabilities.TopPReasoningLevels = nil
	}
}

func resolveReasoningLevelCapabilities(capabilities *ModelCapabilities, model Model) {
	capabilities.Reasoning = normalizeCapabilitySupport(capabilities.Reasoning)
	switch capabilities.Reasoning {
	case CapabilityUnsupported:
		capabilities.ReasoningLevels = CapabilityUnsupported
		capabilities.SupportedReasoningLevels = nil
		return
	case CapabilityUnknown:
		capabilities.ReasoningLevels = CapabilityUnknown
		capabilities.SupportedReasoningLevels = nil
		return
	}

	if capabilities.ReasoningLevels == "" {
		if len(model.ThinkingLevelMap) == 0 {
			capabilities.ReasoningLevels = CapabilityUnknown
			capabilities.SupportedReasoningLevels = nil
			return
		}
		capabilities.SupportedReasoningLevels = explicitReasoningLevels(model.ThinkingLevelMap)
		if len(capabilities.SupportedReasoningLevels) == 0 {
			capabilities.ReasoningLevels = CapabilityUnsupported
			return
		}
		capabilities.ReasoningLevels = CapabilitySupported
	}

	capabilities.ReasoningLevels = normalizeCapabilitySupport(capabilities.ReasoningLevels)
	if capabilities.ReasoningLevels != CapabilitySupported {
		capabilities.SupportedReasoningLevels = nil
		return
	}
	capabilities.SupportedReasoningLevels = normalizeReasoningLevels(capabilities.SupportedReasoningLevels)
	if len(capabilities.SupportedReasoningLevels) == 0 {
		capabilities.ReasoningLevels = CapabilityUnknown
	}
}

func explicitReasoningLevels(levelMap ThinkingLevelMap) []ModelThinkingLevel {
	levels := make([]ModelThinkingLevel, 0, len(levelMap))
	for _, level := range extendedThinkingLevels {
		if mapped, ok := levelMap[level]; ok && mapped != "" {
			levels = append(levels, level)
		}
	}
	return levels
}

func normalizeReasoningLevels(levels []ModelThinkingLevel) []ModelThinkingLevel {
	if len(levels) == 0 {
		return nil
	}
	requested := make(map[ModelThinkingLevel]struct{}, len(levels))
	for _, level := range levels {
		requested[level] = struct{}{}
	}
	result := make([]ModelThinkingLevel, 0, len(requested))
	for _, level := range extendedThinkingLevels {
		if _, ok := requested[level]; ok {
			result = append(result, level)
		}
	}
	return result
}

func normalizeReasoningLevel(level ModelThinkingLevel) ModelThinkingLevel {
	for _, candidate := range extendedThinkingLevels {
		if level == candidate {
			return level
		}
	}
	return ""
}

func mergeModelCapabilities(base, override ModelCapabilities) ModelCapabilities {
	merged := cloneModelCapabilities(base)
	if override.Streaming != "" {
		merged.Streaming = override.Streaming
	}
	if override.Tools != "" {
		merged.Tools = override.Tools
	}
	if override.StrictTools != "" {
		merged.StrictTools = override.StrictTools
	}
	if override.ToolChoice != "" {
		merged.ToolChoice = override.ToolChoice
	}
	if override.Reasoning != "" {
		merged.Reasoning = override.Reasoning
	}
	if override.ReasoningLevels != "" {
		merged.ReasoningLevels = override.ReasoningLevels
	}
	if override.SupportedReasoningLevels != nil {
		merged.SupportedReasoningLevels = append([]ModelThinkingLevel(nil), override.SupportedReasoningLevels...)
	}
	if override.DefaultReasoningLevel != "" {
		merged.DefaultReasoningLevel = override.DefaultReasoningLevel
	}
	if override.Temperature != "" {
		merged.Temperature = override.Temperature
	}
	if override.TemperatureReasoningLevels != nil {
		merged.TemperatureReasoningLevels = append([]ModelThinkingLevel(nil), override.TemperatureReasoningLevels...)
	}
	if override.TopP != "" {
		merged.TopP = override.TopP
	}
	if override.TopPReasoningLevels != nil {
		merged.TopPReasoningLevels = append([]ModelThinkingLevel(nil), override.TopPReasoningLevels...)
	}
	if override.ParallelToolCalls != "" {
		merged.ParallelToolCalls = override.ParallelToolCalls
	}
	return merged
}

func normalizeModelCapabilities(capabilities *ModelCapabilities) {
	capabilities.Streaming = normalizeCapabilitySupport(capabilities.Streaming)
	capabilities.Tools = normalizeCapabilitySupport(capabilities.Tools)
	capabilities.StrictTools = normalizeCapabilitySupport(capabilities.StrictTools)
	capabilities.ToolChoice = normalizeCapabilitySupport(capabilities.ToolChoice)
	capabilities.Reasoning = normalizeCapabilitySupport(capabilities.Reasoning)
	capabilities.ReasoningLevels = normalizeCapabilitySupport(capabilities.ReasoningLevels)
	capabilities.Temperature = normalizeCapabilitySupport(capabilities.Temperature)
	capabilities.TopP = normalizeCapabilitySupport(capabilities.TopP)
	capabilities.ParallelToolCalls = normalizeCapabilitySupport(capabilities.ParallelToolCalls)
}

func normalizeCapabilitySupport(support CapabilitySupport) CapabilitySupport {
	switch support {
	case CapabilitySupported, CapabilityUnsupported, CapabilityUnknown:
		return support
	default:
		return CapabilityUnknown
	}
}

func capabilitySupport(supported bool) CapabilitySupport {
	if supported {
		return CapabilitySupported
	}
	return CapabilityUnsupported
}

func supportedHostedToolTypes(provider, model HostedToolCapabilities) []HostedToolType {
	all := []HostedToolType{
		HostedToolTypeWebSearch,
		HostedToolTypeFetch,
		HostedToolTypeCodeRunner,
		HostedToolTypeExcel,
	}
	result := make([]HostedToolType, 0, len(all))
	for _, toolType := range all {
		if provider.Supports(toolType) || model.Supports(toolType) {
			result = append(result, toolType)
		}
	}
	return result
}

func cloneModelCapabilities(capabilities ModelCapabilities) ModelCapabilities {
	cloned := capabilities
	cloned.SupportedReasoningLevels = append([]ModelThinkingLevel(nil), capabilities.SupportedReasoningLevels...)
	cloned.TemperatureReasoningLevels = append([]ModelThinkingLevel(nil), capabilities.TemperatureReasoningLevels...)
	cloned.TopPReasoningLevels = append([]ModelThinkingLevel(nil), capabilities.TopPReasoningLevels...)
	return cloned
}
