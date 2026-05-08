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
	if contains(model.ID, "opus-4-6") || contains(model.ID, "opus-4.6") {
		return true
	}
	return false
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
	return cloned
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
