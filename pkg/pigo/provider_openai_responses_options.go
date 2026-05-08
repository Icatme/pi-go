package pigo

import "strings"

type OpenAIResponsesProviderOptions struct {
	CommonProviderOptions
	ReasoningEffort    ThinkingLevel
	ReasoningSummary   string
	ServiceTier        string
	ToolChoice         string
	PreviousResponseID string
	Truncation         string
}

func buildOpenAIResponsesProviderStreamOptions(model Model, options SimpleStreamOptions) ProviderStreamOptions {
	return buildOpenAIResponsesProviderOptions(model, options).toProviderStreamOptions()
}

func normalizeOpenAIResponsesProviderStreamOptions(model Model, options ProviderStreamOptions) ProviderStreamOptions {
	return resolveOpenAIResponsesProviderOptions(model, options).toProviderStreamOptions()
}

func buildOpenAIResponsesProviderOptions(model Model, options SimpleStreamOptions) OpenAIResponsesProviderOptions {
	providerOptions := OpenAIResponsesProviderOptions{
		CommonProviderOptions: buildCommonProviderOptions(model, options),
		ServiceTier:           options.ServiceTier,
		PreviousResponseID:    options.PreviousResponseID,
		Truncation:            options.Truncation,
	}
	if SupportsXHigh(model) {
		providerOptions.ReasoningEffort = options.Reasoning
	} else {
		providerOptions.ReasoningEffort = clampReasoning(options.Reasoning)
	}
	if providerOptions.ReasoningEffort != "" {
		providerOptions.ReasoningSummary = defaultReasoningSummary("")
	}
	return resolveOpenAIResponsesProviderOptions(model, providerOptions.toProviderStreamOptions())
}

func resolveOpenAIResponsesProviderOptions(model Model, options ProviderStreamOptions) OpenAIResponsesProviderOptions {
	common := commonProviderOptionsFromStream(options)
	resolved := OpenAIResponsesProviderOptions{
		CommonProviderOptions: common,
		ReasoningEffort:       options.Reasoning,
		ReasoningSummary:      options.ReasoningSummary,
		ServiceTier:           options.ServiceTier,
		ToolChoice:            options.ToolChoice,
		PreviousResponseID:    options.PreviousResponseID,
		Truncation:            options.Truncation,
	}

	if resolved.ReasoningEffort != "" {
		resolved.ReasoningEffort = ThinkingLevel(clampOpenAIResponsesReasoningEffort(model, resolved.ReasoningEffort))
	}
	if strings.TrimSpace(resolved.ReasoningSummary) == "" && resolved.ReasoningEffort != "" {
		resolved.ReasoningSummary = defaultReasoningSummary("")
	}

	return resolved
}

func (options OpenAIResponsesProviderOptions) toProviderStreamOptions() ProviderStreamOptions {
	streamOptions := options.CommonProviderOptions.toProviderStreamOptions()
	streamOptions.Reasoning = options.ReasoningEffort
	streamOptions.ReasoningSummary = options.ReasoningSummary
	streamOptions.ServiceTier = options.ServiceTier
	streamOptions.ToolChoice = options.ToolChoice
	streamOptions.PreviousResponseID = options.PreviousResponseID
	streamOptions.Truncation = options.Truncation
	return streamOptions
}
