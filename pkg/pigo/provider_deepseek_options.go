package pigo

func buildDeepSeekProviderStreamOptions(model Model, options SimpleStreamOptions) ProviderStreamOptions {
	return buildDeepSeekProviderOptions(model, options).toProviderStreamOptions(model)
}

func normalizeDeepSeekProviderStreamOptions(model Model, options ProviderStreamOptions) ProviderStreamOptions {
	return resolveDeepSeekProviderOptions(model, options).toProviderStreamOptions(model)
}

func buildDeepSeekProviderOptions(model Model, options SimpleStreamOptions) DeepSeekProviderOptions {
	streamOptions := streamOptionsFromSimple(model, options)
	if model.Reasoning && streamOptions.Reasoning == "" {
		streamOptions.Reasoning = ThinkingLevelXHigh
	}
	streamOptions = streamOptions.withCommonSnapshot(model)
	return DeepSeekProviderOptions{
		StreamOptions: streamOptions,
	}
}

func resolveDeepSeekProviderOptions(model Model, options ProviderStreamOptions) DeepSeekProviderOptions {
	streamOptions := streamOptionsFromProvider(model, options)
	if model.Reasoning && streamOptions.Reasoning == "" {
		streamOptions.Reasoning = ThinkingLevelXHigh
	}
	streamOptions = streamOptions.withCommonSnapshot(model)
	return DeepSeekProviderOptions{
		StreamOptions: streamOptions,
	}
}

func (options DeepSeekProviderOptions) toProviderStreamOptions(model Model) ProviderStreamOptions {
	return options.StreamOptions.providerStreamOptions(model)
}

type DeepSeekProviderOptions struct {
	StreamOptions
}
