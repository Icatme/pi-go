package pigo

type commandCodeModelSpec struct {
	ID            string
	Name          string
	ContextWindow int
	API           API
}

// CommandCodeCompat selects the public wire protocol advertised by model
// discovery. An empty API uses the provider's legacy name-based selection.
type CommandCodeCompat struct {
	API API
}

func (c *CommandCodeCompat) compatAPI() string { return "commandcode-custom" }

// Command Code's public chat-model catalog, fetched 2026-10-14.
// RefreshCommandCodeModels replaces this deterministic initialization snapshot.
var commandCodeModelSpecs = []commandCodeModelSpec{
	{ID: "claude-sonnet-5-5", Name: "Claude Sonnet 5.5", ContextWindow: 1000000, API: "anthropic-messages"},
	{ID: "claude-sonnet-5", Name: "Claude Sonnet 5", ContextWindow: 1000000, API: "anthropic-messages"},
	{ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6", ContextWindow: 1000000, API: "anthropic-messages"},
	{ID: "claude-fable-5-1", Name: "Claude Fable 5.1", ContextWindow: 1000000, API: "anthropic-messages"},
	{ID: "claude-fable-5", Name: "Claude Fable 5", ContextWindow: 1000000, API: "anthropic-messages"},
	{ID: "claude-opus-5-5", Name: "Claude Opus 5.5", ContextWindow: 1000000, API: "anthropic-messages"},
	{ID: "claude-opus-5", Name: "Claude Opus 5", ContextWindow: 1000000, API: "anthropic-messages"},
	{ID: "claude-opus-4-8", Name: "Claude Opus 4.8", ContextWindow: 1000000, API: "anthropic-messages"},
	{ID: "claude-opus-4-7", Name: "Claude Opus 4.7", ContextWindow: 1000000, API: "anthropic-messages"},
	{ID: "claude-haiku-5-5", Name: "Claude Haiku 5.5", ContextWindow: 1000000, API: "anthropic-messages"},
	{ID: "claude-haiku-4-5-20251001", Name: "Claude Haiku 4.5", ContextWindow: 200000, API: "anthropic-messages"},
	{ID: "gpt-6-astra", Name: "GPT-6 Astra", ContextWindow: 1050000, API: "openai-responses"},
	{ID: "gpt-6.1-sol", Name: "GPT-6.1 Sol", ContextWindow: 1050000, API: "openai-responses"},
	{ID: "gpt-6-sol", Name: "GPT-6 Sol", ContextWindow: 1050000, API: "openai-responses"},
	{ID: "gpt-6-luna", Name: "GPT-6 Luna", ContextWindow: 1050000, API: "openai-responses"},
	{ID: "gpt-5.6-sol", Name: "GPT-5.6 Sol", ContextWindow: 1050000, API: "openai-responses"},
	{ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra", ContextWindow: 1050000, API: "openai-responses"},
	{ID: "gpt-5.6-luna", Name: "GPT-5.6 Luna", ContextWindow: 1050000, API: "openai-responses"},
	{ID: "gpt-5.5", Name: "GPT-5.5", ContextWindow: 400000, API: "openai-responses"},
	{ID: "gpt-5.4", Name: "GPT-5.4", ContextWindow: 400000, API: "openai-responses"},
	{ID: "gpt-5.3-codex", Name: "GPT-5.3 Codex", ContextWindow: 400000, API: "openai-responses"},
	{ID: "gpt-5.4-mini", Name: "GPT-5.4 Mini", ContextWindow: 400000, API: "openai-responses"},
	{ID: "deepseek/deepseek-v4-pro", Name: "DeepSeek V4 Pro (latest)", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "deepseek/deepseek-v4-flash", Name: "DeepSeek V4 Flash (latest)", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "deepseek/deepseek-v4-flash-vision-exp", Name: "DeepSeek V4 Flash Vision (exp)", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "deepseek/deepseek-v4-flash-fast", Name: "DeepSeek V4 Flash Fast", ContextWindow: 1000000, API: "openai-completions"},
	{ID: "deepseek/deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "deepseek/deepseek-v4.1-flash-fast", Name: "DeepSeek V4.1 Flash Fast", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "moonshotai/Kimi-K3", Name: "Kimi K3", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "moonshotai/Kimi-K2.7-Code", Name: "Kimi K2.7 Code", ContextWindow: 256000, API: "openai-responses"},
	{ID: "moonshotai/Kimi-K2.7-Code-Highspeed", Name: "Kimi K2.7 Code HighSpeed", ContextWindow: 262000, API: "openai-responses"},
	{ID: "moonshotai/Kimi-K2.6", Name: "Kimi K2.6", ContextWindow: 256000, API: "openai-responses"},
	{ID: "moonshotai/Kimi-K2.5", Name: "Kimi K2.5", ContextWindow: 256000, API: "openai-responses"},
	{ID: "z-ai/glm-5.3-flash", Name: "GLM-5.3 Flash", ContextWindow: 1048576, API: "openai-responses"},
	{ID: "z-ai/glm-5.3-flashx", Name: "GLM-5.3 FlashX", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "zai-org/GLM-5.3", Name: "GLM-5.3", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "zai-org/GLM-5.2", Name: "GLM-5.2", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "zai-org/GLM-5.2-Fast", Name: "GLM-5.2 Fast", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "zai-org/GLM-5.1", Name: "GLM-5.1", ContextWindow: 200000, API: "openai-responses"},
	{ID: "zai-org/GLM-5", Name: "GLM-5", ContextWindow: 200000, API: "openai-responses"},
	{ID: "MiniMaxAI/MiniMax-M3", Name: "MiniMax M3", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "MiniMaxAI/MiniMax-M2.7", Name: "MiniMax M2.7", ContextWindow: 200000, API: "openai-responses"},
	{ID: "MiniMaxAI/MiniMax-M2.5", Name: "MiniMax M2.5", ContextWindow: 200000, API: "openai-responses"},
	{ID: "xiaomi/mimo-v2.6-pro", Name: "MiMo V2.6 Pro", ContextWindow: 1048576, API: "openai-responses"},
	{ID: "xiaomi/mimo-v2.6-pro-ultraspeed", Name: "MiMo V2.6 Pro UltraSpeed", ContextWindow: 1048576, API: "openai-responses"},
	{ID: "xiaomi/mimo-v2.6-flash", Name: "MiMo V2.6 Flash", ContextWindow: 1048576, API: "openai-responses"},
	{ID: "Qwen/Qwen3.8-Omni-Flash", Name: "Qwen 3.8 Omni Flash", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "Qwen/Qwen3.8-Max-0902", Name: "Qwen 3.8 Max 0902", ContextWindow: 1000000, API: "openai-completions"},
	{ID: "Qwen/Qwen3.8-Max", Name: "Qwen 3.8 Max", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "Qwen/Qwen3.8-27B", Name: "Qwen 3.8 27B", ContextWindow: 262144, API: "openai-responses"},
	{ID: "Qwen/Qwen3.8-Flash", Name: "Qwen 3.8 Flash", ContextWindow: 1000000, API: "openai-completions"},
	{ID: "Qwen/Qwen3.7-Max", Name: "Qwen 3.7 Max", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "Qwen/Qwen3.7-Plus", Name: "Qwen 3.7 Plus", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "Qwen/Qwen3.7-Flash", Name: "Qwen 3.7 Flash", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "Qwen/Qwen3.6-Max-Preview", Name: "Qwen 3.6 Max Preview", ContextWindow: 200000, API: "openai-responses"},
	{ID: "Qwen/Qwen3.6-Plus", Name: "Qwen 3.6 Plus", ContextWindow: 200000, API: "openai-responses"},
	{ID: "meituan/LongCat-2.0", Name: "LongCat 2.0", ContextWindow: 1048576, API: "openai-completions"},
	{ID: "stepfun/Step-5-Preview", Name: "Step 5 Preview", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "stepfun/Step-3.7-Flash", Name: "Step 3.7 Flash", ContextWindow: 256000, API: "openai-responses"},
	{ID: "stepfun/Step-3.5-Flash", Name: "Step 3.5 Flash", ContextWindow: 262144, API: "openai-responses"},
	{ID: "tencent/hy3-paid", Name: "Tencent Hy3", ContextWindow: 262144, API: "openai-responses"},
	{ID: "tencent/hy4-preview", Name: "Tencent Hy4 Preview", ContextWindow: 1048576, API: "openai-completions"},
	{ID: "google/gemini-4-argon", Name: "Gemini 4 Argon", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "google/gemini-3.8-flash", Name: "Gemini 3.8 Flash", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "google/gemini-3.7-flash", Name: "Gemini 3.7 Flash", ContextWindow: 1048576, API: "openai-completions"},
	{ID: "google/gemini-3.6-flash", Name: "Gemini 3.6 Flash", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "google/gemini-3.5-flash", Name: "Gemini 3.5 Flash", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "google/gemini-3.5-flash-lite", Name: "Gemini 3.5 Flash Lite", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "google/gemini-3.1-flash-lite", Name: "Gemini 3.1 Flash Lite", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "sakana/fugu-ultra", Name: "Fugu Ultra", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "nvidia/nemotron-3-ultra-550b-a55b", Name: "Nemotron 3 Ultra", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "thinkingmachines/inkling", Name: "Inkling", ContextWindow: 256000, API: "openai-responses"},
	{ID: "thinkingmachines/inkling-small", Name: "Inkling Small", ContextWindow: 1000000, API: "openai-responses"},
	{ID: "stealth/glyph-cluster:free", Name: "Glyph Cluster", ContextWindow: 256000, API: "openai-responses"},
	{ID: "poolside/laguna-s-2.1-free", Name: "Laguna S 2.1", ContextWindow: 256000, API: "openai-responses"},
	{ID: "meta/muse-spark-1.1", Name: "Muse Spark 1.1", ContextWindow: 1048576, API: "openai-responses"},
	{ID: "meta/muse-spark-1.2", Name: "Muse Spark 1.2", ContextWindow: 1048576, API: "openai-responses"},
	{ID: "meta/muse-spark-1.2-contributor", Name: "Muse Spark 1.2 Contributor", ContextWindow: 1048576, API: "openai-responses"},
	{ID: "meta/muse-spark-1.3", Name: "Muse Spark 1.3", ContextWindow: 1048576, API: "openai-responses"},
	{ID: "meta/muse-spark-1.3-contributor", Name: "Muse Spark 1.3 Contributor", ContextWindow: 1048576, API: "openai-responses"},
	{ID: "meta/muse-wonder-1.0", Name: "Muse Wonder", ContextWindow: 1048576, API: "openai-responses"},
	{ID: "xai/grok-4.5", Name: "Grok 4.5", ContextWindow: 500000, API: "openai-responses"},
	{ID: "xai/grok-4.6", Name: "Grok 4.6", ContextWindow: 500000, API: "openai-responses"},
	{ID: "xai/grok-4.7", Name: "Grok 4.7", ContextWindow: 500000, API: "openai-responses"},
	{ID: "mistral/mistral-large-4", Name: "Mistral Large 4", ContextWindow: 524288, API: "openai-responses"},
}

func newCommandCodeProviderModule() ProviderModule {
	return newCommandCodeProviderModuleWithModels(commandCodeModelsFromSpecs(commandCodeModelSpecs, true))
}

func newCommandCodeProviderModuleWithModels(models map[string]Model) ProviderModule {
	return ProviderModule{
		Provider: "commandcode",
		Quota:    commandCodeQuotaQuerier{},
		Auth: ProviderAuth{
			EnvAPIKeyName:  "COMMAND_CODE_API_KEY",
			EnvAPIKeyNames: []string{"COMMAND_CODE_API_KEY", "COMMANDCODE_API_KEY"},
		},
		Capabilities: ProviderCapabilities{
			SupportsStreaming: true,
		},
		ModelCapabilities: ModelCapabilities{
			Tools:           CapabilityUnknown,
			Reasoning:       CapabilityUnknown,
			ReasoningLevels: CapabilityUnknown,
		},
		BuildOptions:     buildCommandCodeProviderStreamOptions,
		NormalizeOptions: normalizeCommandCodeProviderStreamOptions,
		Models:           models,
	}
}

func commandCodeModelsFromSpecs(specs []commandCodeModelSpec, requireKnownCost bool) map[string]Model {
	models := make(map[string]Model, len(specs))
	for _, spec := range specs {
		cost, ok := commandCodeModelCosts[spec.ID]
		if !ok && requireKnownCost {
			panic("missing Command Code pricing for model " + spec.ID)
		}
		model := newCommandCodeModel(
			spec.ID,
			spec.Name+" (CC)",
			spec.ContextWindow,
			minInt(spec.ContextWindow, commandCodeDefaultModelMaxTokens),
			cost,
		)
		if spec.API != "" {
			model.Compat = &CommandCodeCompat{API: spec.API}
		}
		models[spec.ID] = model
	}
	return models
}

func newCommandCodeModel(id, name string, contextWindow, maxTokens int, cost UsageCost) Model {
	metadata, known := commandCodeModelCatalog[id]
	reasoning := known && metadata.Reasoning
	capabilities := ModelCapabilities{Reasoning: CapabilityUnknown, ReasoningLevels: CapabilityUnknown}
	if known {
		capabilities = ModelCapabilities{Reasoning: capabilitySupport(reasoning), ReasoningLevels: capabilitySupport(len(metadata.Efforts) > 0)}
	}
	input := []InputType{InputText}
	thinking := ThinkingLevelMap{}
	// Upstream leaves off unmapped so it remains supported independently of effort levels.
	for _, level := range []ModelThinkingLevel{ModelThinkingLevelMinimal, ModelThinkingLevelLow, ModelThinkingLevelMedium, ModelThinkingLevelHigh, ModelThinkingLevelXHigh, ModelThinkingLevelMax} {
		thinking[level] = ""
	}
	if known {
		input = append([]InputType(nil), metadata.Input...)
		maxTokens = minInt(contextWindow, metadata.MaxOutputTokens)
		for _, level := range metadata.Efforts {
			thinking[level] = string(level)
		}
	}
	return Model{
		ID:               id,
		Name:             name,
		API:              "commandcode-custom",
		Provider:         "commandcode",
		BaseURL:          resolveCommandCodeAPIBaseURL(),
		Reasoning:        reasoning,
		Input:            input,
		ThinkingLevelMap: thinking,
		CostTiers:        append([]ModelCostTier(nil), commandCodeModelCostTiers[id]...),
		Capabilities:     capabilities,
		Cost:             cost,
		ContextWindow:    contextWindow,
		MaxTokens:        maxTokens,
		Headers: map[string]string{

			"User-Agent":             "cli",
			"x-cli-environment":      "production",
			"x-command-code-version": commandCodeCLIVersion,
		},
	}
}
