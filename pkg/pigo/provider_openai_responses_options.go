package pigo

import "strings"

type OpenAIResponsesProviderOptions struct {
	StreamOptions
}

func buildOpenAIResponsesProviderStreamOptions(model Model, options SimpleStreamOptions) ProviderStreamOptions {
	return buildOpenAIResponsesProviderOptions(model, options).toProviderStreamOptions(model)
}

func normalizeOpenAIResponsesProviderStreamOptions(model Model, options ProviderStreamOptions) ProviderStreamOptions {
	return resolveOpenAIResponsesProviderOptions(model, options).toProviderStreamOptions(model)
}

func buildOpenAIResponsesProviderOptions(model Model, options SimpleStreamOptions) OpenAIResponsesProviderOptions {
	providerOptions := OpenAIResponsesProviderOptions{
		StreamOptions: streamOptionsFromSimple(model, options),
	}
	if SupportsXHigh(model) {
		providerOptions.Reasoning = options.Reasoning
	} else {
		providerOptions.Reasoning = clampReasoning(options.Reasoning)
	}
	if providerOptions.Reasoning != "" {
		providerOptions.ReasoningSummary = defaultReasoningSummary("")
	}
	return resolveOpenAIResponsesProviderOptions(model, providerOptions.toProviderStreamOptions(model))
}

func resolveOpenAIResponsesProviderOptions(model Model, options ProviderStreamOptions) OpenAIResponsesProviderOptions {
	resolved := OpenAIResponsesProviderOptions{
		StreamOptions: streamOptionsFromProvider(model, options),
	}

	if resolved.Reasoning != "" {
		resolved.Reasoning = ThinkingLevel(clampOpenAIResponsesReasoningEffort(model, resolved.Reasoning))
	}
	if strings.TrimSpace(resolved.ReasoningSummary) == "" && resolved.Reasoning != "" {
		resolved.ReasoningSummary = defaultReasoningSummary("")
	}

	return resolved
}

func (options OpenAIResponsesProviderOptions) toProviderStreamOptions(model Model) ProviderStreamOptions {
	return options.StreamOptions.providerStreamOptions(model)
}
