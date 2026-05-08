package pigo

import "strings"

type OpenAICodexProviderOptions struct {
	CommonProviderOptions
	ReasoningEffort    ThinkingLevel
	ReasoningSummary   string
	ServiceTier        string
	TextVerbosity      string
	ToolChoice         string
	PreviousResponseID string
	Truncation         string
}

func buildOpenAICodexProviderStreamOptions(model Model, options SimpleStreamOptions) ProviderStreamOptions {
	return buildOpenAICodexProviderOptions(model, options).toProviderStreamOptions()
}

func normalizeOpenAICodexProviderStreamOptions(model Model, options ProviderStreamOptions) ProviderStreamOptions {
	return resolveOpenAICodexProviderOptions(model, options).toProviderStreamOptions()
}

func buildOpenAICodexProviderOptions(model Model, options SimpleStreamOptions) OpenAICodexProviderOptions {
	providerOptions := OpenAICodexProviderOptions{
		CommonProviderOptions: buildCommonProviderOptions(model, options),
		ServiceTier:           options.ServiceTier,
		TextVerbosity:         defaultTextVerbosity(""),
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
	return resolveOpenAICodexProviderOptions(model, providerOptions.toProviderStreamOptions())
}

func resolveOpenAICodexProviderOptions(model Model, options ProviderStreamOptions) OpenAICodexProviderOptions {
	common := commonProviderOptionsFromStream(options)
	resolved := OpenAICodexProviderOptions{
		CommonProviderOptions: common,
		ReasoningEffort:       options.Reasoning,
		ReasoningSummary:      options.ReasoningSummary,
		ServiceTier:           options.ServiceTier,
		TextVerbosity:         options.TextVerbosity,
		ToolChoice:            options.ToolChoice,
		PreviousResponseID:    options.PreviousResponseID,
		Truncation:            options.Truncation,
	}

	if resolved.ReasoningEffort != "" {
		resolved.ReasoningEffort = ThinkingLevel(clampOpenAICodexReasoningEffort(model, resolved.ReasoningEffort))
	}
	if strings.TrimSpace(resolved.TextVerbosity) == "" {
		resolved.TextVerbosity = defaultTextVerbosity("")
	}
	if strings.TrimSpace(resolved.ReasoningSummary) == "" && resolved.ReasoningEffort != "" {
		resolved.ReasoningSummary = defaultReasoningSummary("")
	}

	return resolved
}

func (options OpenAICodexProviderOptions) toProviderStreamOptions() ProviderStreamOptions {
	streamOptions := options.CommonProviderOptions.toProviderStreamOptions()
	streamOptions.Reasoning = options.ReasoningEffort
	streamOptions.ReasoningSummary = options.ReasoningSummary
	streamOptions.ServiceTier = options.ServiceTier
	streamOptions.TextVerbosity = options.TextVerbosity
	streamOptions.ToolChoice = options.ToolChoice
	streamOptions.PreviousResponseID = options.PreviousResponseID
	streamOptions.Truncation = options.Truncation
	return streamOptions
}
