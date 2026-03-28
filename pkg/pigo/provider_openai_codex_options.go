package pigo

import "strings"

type OpenAICodexProviderOptions struct {
	CommonProviderOptions
	ReasoningEffort  ThinkingLevel
	ReasoningSummary string
	TextVerbosity    string
	ToolChoice       string
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
		TextVerbosity:         defaultTextVerbosity(""),
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
		TextVerbosity:         options.TextVerbosity,
		ToolChoice:            options.ToolChoice,
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
	if resolved.CacheRetention != "" {
		resolved.CacheRetention = CacheRetentionNone
	}

	return resolved
}

func (options OpenAICodexProviderOptions) toProviderStreamOptions() ProviderStreamOptions {
	streamOptions := options.CommonProviderOptions.toProviderStreamOptions()
	streamOptions.Reasoning = options.ReasoningEffort
	streamOptions.ReasoningSummary = options.ReasoningSummary
	streamOptions.TextVerbosity = options.TextVerbosity
	streamOptions.ToolChoice = options.ToolChoice
	return streamOptions
}
