package pigo

const tokenHarborBaseURL = "https://tokenharbor.ai/v1"

// Token Harbor documents this provider name, endpoint, and API-key variable:
// https://tokenharbor.ai/docs/integrations/pi
// Named models use Chat Completions without Orchestra's prompt steering:
// https://tokenharbor.ai/docs/api/curl
// These catalog limits describe the upstream models, not account entitlement
// or a live guarantee of the gateway's available capacity.
func newTokenHarborProviderModule() ProviderModule {
	models := map[string]Model{
		// https://api-docs.deepseek.com/quick_start/pricing/
		"deepseek-v4-flash": {
			Name: "DeepSeek V4 Flash", ContextWindow: 1_000_000, MaxTokens: 384_000,
			Input: []InputType{InputText},
		},
		// The exact pinned pi-go GPT-5.6 catalog supplies the upstream limits.
		"gpt-5.6-luna": {
			Name: "GPT-5.6 Luna", ContextWindow: 1_050_000, MaxTokens: 128_000,
			Input:            []InputType{InputText, InputImage},
			ThinkingLevelMap: ThinkingLevelMap{ModelThinkingLevelOff: "none"},
		},
		// https://docs.z.ai/guides/vlm/glm-5.3-flash
		// GLM-5.3-Flash always reasons; disabling thinking is unsupported.
		"glm-5.3-flash": {
			Name: "GLM 5.3 Flash", ContextWindow: 1_000_000, MaxTokens: 128_000,
			Input: []InputType{InputText, InputImage},
		},
	}
	for id, model := range models {
		model.ID = id
		model.API = "openai-completions"
		model.BaseURL = tokenHarborBaseURL
		model.Reasoning = true
		no := false
		supportsEffort := id == "gpt-5.6-luna"
		model.Compat = &OpenAICompletionsCompat{
			SupportsStore: &no, SupportsDeveloperRole: &no, SupportsReasoningEffort: &supportsEffort,
			MaxTokensField: "max_tokens", SupportsLongCacheRetention: &no,
		}
		if id == "gpt-5.6-luna" {
			model.Compat.(*OpenAICompletionsCompat).MaxTokensField = "max_completion_tokens"
		}
		models[id] = model
	}
	return ProviderModule{
		Provider:     "tokenharbor",
		Auth:         ProviderAuth{EnvAPIKeyName: "TOKENHARBOR_API_KEY"},
		Capabilities: ProviderCapabilities{SupportsStreaming: true, SupportsToolChoice: true},
		ModelCapabilities: ModelCapabilities{
			Tools: CapabilitySupported, ToolChoice: CapabilitySupported,
			StrictTools: CapabilityUnsupported,
			Reasoning:   CapabilitySupported, ReasoningLevels: CapabilityUnsupported,
			Temperature: CapabilityUnsupported, TopP: CapabilityUnsupported,
			ParallelToolCalls: CapabilityUnsupported,
		},
		Models: models,
	}
}
