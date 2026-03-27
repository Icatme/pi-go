package pigo

import "strings"

func NormalizeCompleteOptions(model Model, options CompleteOptions) CompleteOptions {
	module := resolveProviderModule(model.Provider)
	if module == nil || module.NormalizeOptions == nil {
		return options
	}
	return module.NormalizeOptions(model, options)
}

func normalizeOpenAICodexOptions(model Model, options CompleteOptions) CompleteOptions {
	if options.Reasoning != "" {
		options.Reasoning = ThinkingLevel(clampOpenAICodexReasoningEffort(model, options.Reasoning))
	}
	if strings.TrimSpace(options.TextVerbosity) == "" {
		options.TextVerbosity = defaultTextVerbosity("")
	}
	if strings.TrimSpace(options.ReasoningSummary) == "" && options.Reasoning != "" {
		options.ReasoningSummary = defaultReasoningSummary("")
	}
	if options.CacheRetention != "" {
		options.CacheRetention = CacheRetentionNone
	}
	return options
}

func normalizeKimiCodingOptions(_ Model, options CompleteOptions) CompleteOptions {
	options.ToolChoice = ""
	if options.TextVerbosity != "" {
		options.TextVerbosity = ""
	}
	if options.ReasoningSummary != "" {
		options.ReasoningSummary = ""
	}
	if options.SessionID != "" {
		options.SessionID = ""
	}
	return options
}
