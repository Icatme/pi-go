package pigo

type commandCodeModelSpec struct {
	ID            string
	Name          string
	ContextWindow int
}

// Command Code's Provider API is the authority for availability. This
// 2026-07-29 snapshot keeps bare library initialization deterministic; the CLI
// and RefreshCommandCodeModels replace it with the validated live catalog.
var commandCodeModelSpecs = []commandCodeModelSpec{
	{ID: "claude-sonnet-5", Name: "Claude Sonnet 5", ContextWindow: 1000000},
	{ID: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6", ContextWindow: 1000000},
	{ID: "claude-fable-5", Name: "Claude Fable 5", ContextWindow: 1000000},
	{ID: "claude-opus-5", Name: "Claude Opus 5", ContextWindow: 1000000},
	{ID: "claude-opus-4-8", Name: "Claude Opus 4.8", ContextWindow: 1000000},
	{ID: "claude-opus-4-7", Name: "Claude Opus 4.7", ContextWindow: 1000000},
	{ID: "claude-haiku-4-5-20251001", Name: "Claude Haiku 4.5", ContextWindow: 200000},
	{ID: "gpt-5.6-sol", Name: "GPT-5.6 Sol", ContextWindow: 1050000},
	{ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra", ContextWindow: 1050000},
	{ID: "gpt-5.6-luna", Name: "GPT-5.6 Luna", ContextWindow: 1050000},
	{ID: "gpt-5.5", Name: "GPT-5.5", ContextWindow: 200000},
	{ID: "gpt-5.4", Name: "GPT-5.4", ContextWindow: 400000},
	{ID: "gpt-5.3-codex", Name: "GPT-5.3 Codex", ContextWindow: 400000},
	{ID: "gpt-5.4-mini", Name: "GPT-5.4 Mini", ContextWindow: 400000},
	{ID: "deepseek/deepseek-v4-pro", Name: "DeepSeek V4 Pro", ContextWindow: 1000000},
	{ID: "deepseek/deepseek-v4-flash", Name: "DeepSeek V4 Flash", ContextWindow: 1000000},
	{ID: "moonshotai/Kimi-K3", Name: "Kimi K3", ContextWindow: 1000000},
	{ID: "moonshotai/Kimi-K2.7-Code", Name: "Kimi K2.7 Code", ContextWindow: 256000},
	{ID: "moonshotai/Kimi-K2.7-Code-Highspeed", Name: "Kimi K2.7 Code HighSpeed", ContextWindow: 262000},
	{ID: "moonshotai/Kimi-K2.6", Name: "Kimi K2.6", ContextWindow: 256000},
	{ID: "moonshotai/Kimi-K2.5", Name: "Kimi K2.5", ContextWindow: 256000},
	{ID: "zai-org/GLM-5.2", Name: "GLM-5.2", ContextWindow: 1000000},
	{ID: "zai-org/GLM-5.2-Fast", Name: "GLM-5.2 Fast", ContextWindow: 1000000},
	{ID: "zai-org/GLM-5.1", Name: "GLM-5.1", ContextWindow: 200000},
	{ID: "zai-org/GLM-5", Name: "GLM-5", ContextWindow: 200000},
	{ID: "MiniMaxAI/MiniMax-M3", Name: "MiniMax M3", ContextWindow: 1000000},
	{ID: "MiniMaxAI/MiniMax-M2.7", Name: "MiniMax M2.7", ContextWindow: 200000},
	{ID: "MiniMaxAI/MiniMax-M2.5", Name: "MiniMax M2.5", ContextWindow: 200000},
	{ID: "xiaomi/mimo-v2.5-pro", Name: "MiMo V2.5 Pro", ContextWindow: 1000000},
	{ID: "xiaomi/mimo-v2.5", Name: "MiMo V2.5", ContextWindow: 1000000},
	{ID: "Qwen/Qwen3.6-Max-Preview", Name: "Qwen 3.6 Max Preview", ContextWindow: 200000},
	{ID: "Qwen/Qwen3.6-Plus", Name: "Qwen 3.6 Plus", ContextWindow: 200000},
	{ID: "Qwen/Qwen3.7-Max", Name: "Qwen 3.7 Max", ContextWindow: 1000000},
	{ID: "Qwen/Qwen3.7-Plus", Name: "Qwen 3.7 Plus", ContextWindow: 1000000},
	{ID: "stepfun/Step-3.7-Flash", Name: "Step 3.7 Flash", ContextWindow: 256000},
	{ID: "stepfun/Step-3.5-Flash", Name: "Step 3.5 Flash", ContextWindow: 1000000},
	{ID: "tencent/hy3-paid", Name: "Tencent Hy3", ContextWindow: 262144},
	{ID: "google/gemini-3.6-flash", Name: "Gemini 3.6 Flash", ContextWindow: 1000000},
	{ID: "google/gemini-3.5-flash", Name: "Gemini 3.5 Flash", ContextWindow: 1000000},
	{ID: "google/gemini-3.5-flash-lite", Name: "Gemini 3.5 Flash Lite", ContextWindow: 1000000},
	{ID: "google/gemini-3.1-flash-lite", Name: "Gemini 3.1 Flash Lite", ContextWindow: 1000000},
	{ID: "sakana/fugu-ultra", Name: "Fugu Ultra", ContextWindow: 1000000},
	{ID: "nvidia/nemotron-3-ultra-550b-a55b", Name: "Nemotron 3 Ultra", ContextWindow: 1000000},
	{ID: "thinkingmachines/inkling", Name: "Inkling", ContextWindow: 256000},
	{ID: "poolside/laguna-s-2.1-free", Name: "Laguna S 2.1", ContextWindow: 256000},
	{ID: "inclusionai/ling-3.0-flash-free", Name: "Ling 3.0 Flash", ContextWindow: 256000},
	{ID: "meta/muse-spark-1.1", Name: "Muse Spark 1.1", ContextWindow: 1048576},
	{ID: "xai/grok-4.5", Name: "Grok 4.5", ContextWindow: 500000},
}

// Pricing is the current Command Code price per million tokens as of 2026-07-29:
// https://commandcode.ai/docs/resources/pricing-limits
// A model must have an explicit entry, including models that are currently free,
// so a missing paid-model price can never silently become a zero-cost model.
var commandCodeModelCosts = map[string]UsageCost{
	"claude-sonnet-5":                     {Input: 2, Output: 10, CacheRead: 0.2, CacheWrite: 2.5},
	"claude-sonnet-4-6":                   {Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75},
	"claude-fable-5":                      {Input: 10, Output: 50, CacheRead: 1, CacheWrite: 12.5},
	"claude-opus-5":                       {Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25},
	"claude-opus-4-8":                     {Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25},
	"claude-opus-4-7":                     {Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25},
	"claude-haiku-4-5-20251001":           {Input: 1, Output: 5, CacheRead: 0.1, CacheWrite: 1.25},
	"gpt-5.6-sol":                         {Input: 5, Output: 30, CacheRead: 0.5, CacheWrite: 6.25},
	"gpt-5.6-terra":                       {Input: 2.5, Output: 15, CacheRead: 0.25, CacheWrite: 3.125},
	"gpt-5.6-luna":                        {Input: 1, Output: 6, CacheRead: 0.1, CacheWrite: 1.25},
	"gpt-5.5":                             {Input: 5, Output: 30, CacheRead: 0.5},
	"gpt-5.4":                             {Input: 2.5, Output: 15, CacheRead: 0.25},
	"gpt-5.3-codex":                       {Input: 2, Output: 8, CacheRead: 0.5},
	"gpt-5.4-mini":                        {Input: 0.75, Output: 4.5, CacheRead: 0.075},
	"deepseek/deepseek-v4-pro":            {Input: 0.435, Output: 0.87, CacheRead: 0.003625},
	"deepseek/deepseek-v4-flash":          {Input: 0.14, Output: 0.28, CacheRead: 0.0028},
	"moonshotai/Kimi-K3":                  {Input: 3, Output: 15, CacheRead: 0.3},
	"moonshotai/Kimi-K2.7-Code":           {Input: 0.95, Output: 4, CacheRead: 0.19},
	"moonshotai/Kimi-K2.7-Code-Highspeed": {Input: 1.9, Output: 8, CacheRead: 0.38},
	"moonshotai/Kimi-K2.6":                {Input: 0.95, Output: 4, CacheRead: 0.16},
	"moonshotai/Kimi-K2.5":                {Input: 0.6, Output: 3, CacheRead: 0.1},
	"zai-org/GLM-5.2":                     {Input: 1.4, Output: 4.4, CacheRead: 0.26},
	"zai-org/GLM-5.2-Fast":                {Input: 3, Output: 10.25, CacheRead: 0.5},
	"zai-org/GLM-5.1":                     {Input: 1.4, Output: 4.4, CacheRead: 0.26},
	"zai-org/GLM-5":                       {Input: 1, Output: 3.2, CacheRead: 0.2},
	"MiniMaxAI/MiniMax-M3":                {Input: 0.3, Output: 1.2, CacheRead: 0.06},
	"MiniMaxAI/MiniMax-M2.7":              {Input: 0.3, Output: 1.2, CacheRead: 0.06},
	"MiniMaxAI/MiniMax-M2.5":              {Input: 0.3, Output: 1.2, CacheRead: 0.03},
	"xiaomi/mimo-v2.5-pro":                {Input: 0.435, Output: 0.87, CacheRead: 0.0036},
	"xiaomi/mimo-v2.5":                    {Input: 0.14, Output: 0.28, CacheRead: 0.0028},
	"Qwen/Qwen3.6-Max-Preview":            {Input: 1.3, Output: 7.8, CacheRead: 0.26, CacheWrite: 1.63},
	"Qwen/Qwen3.6-Plus":                   {Input: 0.5, Output: 3, CacheRead: 0.1},
	"Qwen/Qwen3.7-Max":                    {Input: 2.5, Output: 7.5, CacheRead: 0.5, CacheWrite: 3.13},
	"Qwen/Qwen3.7-Plus":                   {Input: 0.4, Output: 1.6, CacheRead: 0.08, CacheWrite: 0.5},
	"stepfun/Step-3.7-Flash":              {Input: 0.2, Output: 1.15, CacheRead: 0.04},
	"stepfun/Step-3.5-Flash":              {Input: 0.1, Output: 0.3, CacheRead: 0.02},
	"tencent/hy3-paid":                    {Input: 0.14, Output: 0.58, CacheRead: 0.035},
	"google/gemini-3.6-flash":             {Input: 1.5, Output: 7.5, CacheRead: 0.15},
	"google/gemini-3.5-flash":             {Input: 1.5, Output: 9, CacheRead: 0.15},
	"google/gemini-3.5-flash-lite":        {Input: 0.3, Output: 2.5, CacheRead: 0.03},
	"google/gemini-3.1-flash-lite":        {Input: 0.25, Output: 1.5, CacheRead: 0.03},
	"sakana/fugu-ultra":                   {Input: 5, Output: 30, CacheRead: 0.5},
	"nvidia/nemotron-3-ultra-550b-a55b":   {Input: 0.6, Output: 2.4, CacheRead: 0.12},
	"thinkingmachines/inkling":            {Input: 1, Output: 4.05, CacheRead: 0.17},
	"poolside/laguna-s-2.1-free":          {},
	"inclusionai/ling-3.0-flash-free":     {},
	"meta/muse-spark-1.1":                 {Input: 1.25, Output: 4.25, CacheRead: 0.15},
	"xai/grok-4.5":                        {Input: 2, Output: 6, CacheRead: 0.5},
}

var commandCodeLongContextCosts = map[string]struct {
	Threshold int
	Cost      UsageCost
}{
	"Qwen/Qwen3.7-Plus": {
		Threshold: 256_000,
		Cost:      UsageCost{Input: 1.2, Output: 4.8, CacheRead: 0.24, CacheWrite: 1.5},
	},
}

func newCommandCodeProviderModule() ProviderModule {
	return newCommandCodeProviderModuleWithModels(commandCodeModelsFromSpecs(commandCodeModelSpecs, true))
}

func newCommandCodeProviderModuleWithModels(models map[string]Model) ProviderModule {
	return ProviderModule{
		Provider: "commandcode",
		Auth: ProviderAuth{
			EnvAPIKeyName: "COMMANDCODE_API_KEY",
		},
		Capabilities: ProviderCapabilities{
			SupportsStreaming: true,
		},
		BuildOptions:     buildCommandCodeProviderStreamOptions,
		NormalizeOptions: normalizeCommandCodeProviderStreamOptions,
		Models:           models,
	}
}

func commandCodeModelsFromSpecs(specs []commandCodeModelSpec, requireKnownCost bool) map[string]Model {
	baseURL := resolveCommandCodeAPIBaseURL()
	models := make(map[string]Model, len(specs))
	for _, spec := range specs {
		cost, ok := commandCodeModelCosts[spec.ID]
		if !ok && requireKnownCost {
			panic("missing Command Code pricing for model " + spec.ID)
		}
		models[spec.ID] = Model{
			ID:            spec.ID,
			Name:          spec.Name + " (CC)",
			API:           "commandcode-custom",
			Provider:      "commandcode",
			BaseURL:       baseURL,
			Reasoning:     true,
			Input:         []InputType{InputText},
			Cost:          cost,
			ContextWindow: spec.ContextWindow,
			MaxTokens:     minInt(spec.ContextWindow, commandCodeDefaultModelMaxTokens),
			Headers: map[string]string{
				"Accept":                 "*/*",
				"Accept-Encoding":        "gzip, deflate",
				"Accept-Language":        "*",
				"Sec-Fetch-Mode":         "cors",
				"User-Agent":             "node",
				"x-cli-environment":      "production",
				"x-command-code-version": commandCodeCLIVersion,
			},
		}
	}
	return models
}
