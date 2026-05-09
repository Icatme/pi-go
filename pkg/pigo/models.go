package pigo

import "sort"

func GetModel(provider Provider, modelID string) *Model {
	module := resolveProviderModule(provider)
	if module == nil {
		return nil
	}

	model, ok := module.Models[modelID]
	if !ok {
		return nil
	}

	cloned := cloneModel(model)
	return &cloned
}

func GetProviders() []Provider {
	providers := listRegisteredProviders()
	sort.Slice(providers, func(i int, j int) bool {
		return string(providers[i]) < string(providers[j])
	})
	return providers
}

func GetModels(provider Provider) []Model {
	module := resolveProviderModule(provider)
	if module == nil {
		return nil
	}

	modelIDs := make([]string, 0, len(module.Models))
	for modelID := range module.Models {
		modelIDs = append(modelIDs, modelID)
	}
	sort.Strings(modelIDs)

	models := make([]Model, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		models = append(models, cloneModel(module.Models[modelID]))
	}
	return models
}

func CalculateCost(model Model, usage Usage) UsageCost {
	return UsageCost{
		Input:      (model.Cost.Input / 1_000_000) * float64(usage.Input),
		Output:     (model.Cost.Output / 1_000_000) * float64(usage.Output),
		CacheRead:  (model.Cost.CacheRead / 1_000_000) * float64(usage.CacheRead),
		CacheWrite: (model.Cost.CacheWrite / 1_000_000) * float64(usage.CacheWrite),
		Total: ((model.Cost.Input / 1_000_000) * float64(usage.Input)) +
			((model.Cost.Output / 1_000_000) * float64(usage.Output)) +
			((model.Cost.CacheRead / 1_000_000) * float64(usage.CacheRead)) +
			((model.Cost.CacheWrite / 1_000_000) * float64(usage.CacheWrite)),
	}
}

func ModelsAreEqual(a, b *Model) bool {
	if a == nil || b == nil {
		return false
	}
	return a.ID == b.ID && a.Provider == b.Provider
}

func SupportsXHigh(model Model) bool {
	if contains(model.ID, "gpt-5.2") || contains(model.ID, "gpt-5.3") || contains(model.ID, "gpt-5.4") {
		return true
	}
	if contains(model.ID, "opus-4-6") || contains(model.ID, "opus-4.6") || contains(model.ID, "sonnet-4-6") || contains(model.ID, "sonnet-4.6") {
		return true
	}
	return false
}

var extendedThinkingLevels = []ModelThinkingLevel{
	ModelThinkingLevelOff,
	ModelThinkingLevelMinimal,
	ModelThinkingLevelLow,
	ModelThinkingLevelMedium,
	ModelThinkingLevelHigh,
	ModelThinkingLevelXHigh,
}

func GetSupportedThinkingLevels(model Model) []ModelThinkingLevel {
	if !model.Reasoning {
		return []ModelThinkingLevel{ModelThinkingLevelOff}
	}

	var result []ModelThinkingLevel
	for _, level := range extendedThinkingLevels {
		mapped, hasMapping := model.ThinkingLevelMap[level]
		if mapped == "" && hasMapping {
			continue
		}
		if level == ModelThinkingLevelXHigh && !hasMapping {
			continue
		}
		result = append(result, level)
	}
	return result
}

func ClampThinkingLevel(model Model, level ModelThinkingLevel) ModelThinkingLevel {
	available := GetSupportedThinkingLevels(model)
	for _, candidate := range available {
		if candidate == level {
			return level
		}
	}

	requestedIndex := -1
	for i, candidate := range extendedThinkingLevels {
		if candidate == level {
			requestedIndex = i
			break
		}
	}
	if requestedIndex == -1 {
		if len(available) > 0 {
			return available[0]
		}
		return ModelThinkingLevelOff
	}

	for i := requestedIndex; i < len(extendedThinkingLevels); i++ {
		for _, candidate := range available {
			if candidate == extendedThinkingLevels[i] {
				return candidate
			}
		}
	}
	for i := requestedIndex - 1; i >= 0; i-- {
		for _, candidate := range available {
			if candidate == extendedThinkingLevels[i] {
				return candidate
			}
		}
	}
	if len(available) > 0 {
		return available[0]
	}
	return ModelThinkingLevelOff
}

func SupportsHostedTool(model Model, toolType HostedToolType) bool {
	if model.HostedTools.Supports(toolType) {
		return true
	}
	return GetProviderCapabilities(model.Provider).HostedTools.Supports(toolType)
}

func cloneModel(model Model) Model {
	cloned := model
	if len(model.Input) > 0 {
		cloned.Input = append([]InputType(nil), model.Input...)
	}
	if len(model.ThinkingLevelMap) > 0 {
		cloned.ThinkingLevelMap = make(ThinkingLevelMap, len(model.ThinkingLevelMap))
		for k, v := range model.ThinkingLevelMap {
			cloned.ThinkingLevelMap[k] = v
		}
	}
	if len(model.Headers) > 0 {
		cloned.Headers = make(map[string]string, len(model.Headers))
		for k, v := range model.Headers {
			cloned.Headers[k] = v
		}
	}
	cloned.Compat = cloneCompat(model.Compat)
	return cloned
}

func cloneCompat(compat any) any {
	switch typed := compat.(type) {
	case *OpenAICompletionsCompat:
		cloned := *typed
		if typed.OpenRouterRouting != nil {
			routing := *typed.OpenRouterRouting
			routing.Order = cloneStringSlice(typed.OpenRouterRouting.Order)
			routing.Only = cloneStringSlice(typed.OpenRouterRouting.Only)
			routing.Ignore = cloneStringSlice(typed.OpenRouterRouting.Ignore)
			routing.Quantizations = cloneStringSlice(typed.OpenRouterRouting.Quantizations)
			if typed.OpenRouterRouting.MaxPrice != nil {
				routing.MaxPrice = cloneMap(typed.OpenRouterRouting.MaxPrice)
			}
			cloned.OpenRouterRouting = &routing
		}
		if typed.VercelGatewayRouting != nil {
			routing := *typed.VercelGatewayRouting
			routing.Order = cloneStringSlice(typed.VercelGatewayRouting.Order)
			routing.Only = cloneStringSlice(typed.VercelGatewayRouting.Only)
			cloned.VercelGatewayRouting = &routing
		}
		return &cloned
	case *OpenAIResponsesCompat:
		cloned := *typed
		return &cloned
	case *AnthropicMessagesCompat:
		cloned := *typed
		return &cloned
	default:
		return compat
	}
}

func cloneStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func contains(value, needle string) bool {
	return len(needle) > 0 && len(value) >= len(needle) && indexOf(value, needle) >= 0
}

func indexOf(value, needle string) int {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
