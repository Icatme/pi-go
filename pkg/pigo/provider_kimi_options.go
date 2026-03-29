package pigo

type AnthropicMessagesProviderOptions struct {
	CommonProviderOptions
	Reasoning            ThinkingLevel
	ThinkingBudgetTokens int
	ToolChoice           string
}

type KimiCodingProviderOptions struct {
	AnthropicMessagesProviderOptions
}

func buildAnthropicMessagesProviderStreamOptions(model Model, options SimpleStreamOptions) ProviderStreamOptions {
	return buildAnthropicMessagesProviderOptions(model, options).toProviderStreamOptions()
}

func buildAnthropicMessagesProviderOptions(model Model, options SimpleStreamOptions) AnthropicMessagesProviderOptions {
	providerOptions := AnthropicMessagesProviderOptions{
		CommonProviderOptions: buildCommonProviderOptions(model, options),
		Reasoning:             options.Reasoning,
	}
	if providerOptions.Reasoning != "" {
		maxTokens, thinkingBudget := adjustMaxTokensForThinking(
			providerOptions.MaxTokens,
			model.MaxTokens,
			providerOptions.Reasoning,
			options.ThinkingBudgets,
		)
		providerOptions.MaxTokens = maxTokens
		providerOptions.ThinkingBudgetTokens = thinkingBudget
	}
	return providerOptions
}

func resolveAnthropicMessagesProviderOptions(_ Model, options ProviderStreamOptions) AnthropicMessagesProviderOptions {
	return AnthropicMessagesProviderOptions{
		CommonProviderOptions: commonProviderOptionsFromStream(options),
		Reasoning:             options.Reasoning,
		ThinkingBudgetTokens:  options.ThinkingBudgetTokens,
		ToolChoice:            options.ToolChoice,
	}
}

func buildKimiCodingProviderStreamOptions(model Model, options SimpleStreamOptions) ProviderStreamOptions {
	return buildKimiCodingProviderOptions(model, options).toProviderStreamOptions()
}

func normalizeKimiCodingProviderStreamOptions(model Model, options ProviderStreamOptions) ProviderStreamOptions {
	return resolveKimiCodingProviderOptions(model, options).toProviderStreamOptions()
}

func buildKimiCodingProviderOptions(model Model, options SimpleStreamOptions) KimiCodingProviderOptions {
	providerOptions := KimiCodingProviderOptions{
		AnthropicMessagesProviderOptions: buildAnthropicMessagesProviderOptions(model, options),
	}
	providerOptions.SessionID = ""
	providerOptions.ToolChoice = ""
	return providerOptions
}

func resolveKimiCodingProviderOptions(model Model, options ProviderStreamOptions) KimiCodingProviderOptions {
	providerOptions := KimiCodingProviderOptions{
		AnthropicMessagesProviderOptions: resolveAnthropicMessagesProviderOptions(model, options),
	}
	providerOptions.SessionID = ""
	providerOptions.ToolChoice = ""
	return providerOptions
}

func (options AnthropicMessagesProviderOptions) toProviderStreamOptions() ProviderStreamOptions {
	streamOptions := options.CommonProviderOptions.toProviderStreamOptions()
	streamOptions.Reasoning = options.Reasoning
	streamOptions.ThinkingBudgetTokens = options.ThinkingBudgetTokens
	streamOptions.ToolChoice = options.ToolChoice
	return streamOptions
}

func (options KimiCodingProviderOptions) toProviderStreamOptions() ProviderStreamOptions {
	return options.AnthropicMessagesProviderOptions.toProviderStreamOptions()
}
