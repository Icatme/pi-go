package pigo

import "strings"

type OpenAICodexProviderOptions struct {
	StreamOptions
}

func buildOpenAICodexProviderStreamOptions(model Model, options SimpleStreamOptions) ProviderStreamOptions {
	return buildOpenAICodexProviderOptions(model, options).toProviderStreamOptions(model)
}

func normalizeOpenAICodexProviderStreamOptions(model Model, options ProviderStreamOptions) ProviderStreamOptions {
	return resolveOpenAICodexProviderOptions(model, options).toProviderStreamOptions(model)
}

func buildOpenAICodexProviderOptions(model Model, options SimpleStreamOptions) OpenAICodexProviderOptions {
	providerOptions := OpenAICodexProviderOptions{
		StreamOptions: streamOptionsFromSimple(model, options),
	}
	providerOptions.TextVerbosity = defaultTextVerbosity("")
	if SupportsXHigh(model) || SupportsMax(model) {
		providerOptions.Reasoning = options.Reasoning
	} else {
		providerOptions.Reasoning = clampReasoning(options.Reasoning)
	}
	if providerOptions.Reasoning != "" {
		providerOptions.ReasoningSummary = defaultReasoningSummary("")
	}
	return resolveOpenAICodexProviderOptions(model, providerOptions.toProviderStreamOptions(model))
}

func resolveOpenAICodexProviderOptions(model Model, options ProviderStreamOptions) OpenAICodexProviderOptions {
	resolved := OpenAICodexProviderOptions{
		StreamOptions: streamOptionsFromProvider(model, options),
	}

	if resolved.Reasoning != "" {
		resolved.Reasoning = ThinkingLevel(clampOpenAIResponsesReasoningEffort(model, resolved.Reasoning))
	}
	if strings.TrimSpace(resolved.TextVerbosity) == "" {
		resolved.TextVerbosity = defaultTextVerbosity("")
	}
	if strings.TrimSpace(resolved.ReasoningSummary) == "" && resolved.Reasoning != "" {
		resolved.ReasoningSummary = defaultReasoningSummary("")
	}

	return resolved
}

func (options OpenAICodexProviderOptions) toProviderStreamOptions(model Model) ProviderStreamOptions {
	return options.StreamOptions.providerStreamOptions(model)
}
