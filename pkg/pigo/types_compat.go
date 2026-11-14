package pigo

type OpenRouterRouting struct {
	AllowFallbacks         *bool
	RequireParameters      *bool
	DataCollection         string
	ZDR                    *bool
	EnforceDistillableText *bool
	Order                  []string
	Only                   []string
	Ignore                 []string
	Quantizations          []string
	Sort                   any
	MaxPrice               map[string]any
	PreferredMinThroughput any
	PreferredMaxLatency    any
}

type VercelGatewayRouting struct {
	Only  []string
	Order []string
}

type OpenAICompletionsCompat struct {
	// AllowUnauthenticated permits an empty resolved API key for this adapter.
	// Default false. Existing credentials and caller-supplied headers are still
	// used; this is not a policy for suppressing credentials or authorizing URLs.
	AllowUnauthenticated bool

	SupportsMidConvoSystemMessages              *bool
	SupportsStore                               *bool
	SupportsDeveloperRole                       *bool
	SupportsReasoningEffort                     *bool
	SupportsUsageInStreaming                    *bool
	MaxTokensField                              string
	RequiresToolResultName                      *bool
	RequiresAssistantAfterToolResult            *bool
	RequiresThinkingAsText                      *bool
	RequiresReasoningContentOnAssistantMessages *bool
	ThinkingFormat                              string
	OpenRouterRouting                           *OpenRouterRouting
	VercelGatewayRouting                        *VercelGatewayRouting
	ZaiToolStream                               *bool
	SupportsStrictMode                          *bool
	CacheControlFormat                          string
	SendSessionAffinityHeaders                  *bool
	SupportsLongCacheRetention                  *bool
}

func (c *OpenAICompletionsCompat) compatAPI() string { return "openai-completions" }

type OpenAIResponsesCompat struct {
	// SupportsAdditionalTools=false sends the complete current tool set at the
	// top level instead of native additional_tools items. Nil preserves defaults;
	// true does not grant capabilities absent from the model registry.
	SupportsAdditionalTools         *bool
	SupportsDeveloperRole           *bool
	SupportsMidConvoSystemMessages  *bool
	SendSessionIdHeader             *bool
	SupportsLongCacheRetention      *bool
	SupportsExplicitPromptCacheMode *bool
	SupportsMaxOutputTokens         *bool
	SupportsJSONOutput              bool
	SupportsJSONSchema              bool
}

func (c *OpenAIResponsesCompat) compatAPI() string { return "openai-responses" }

type AnthropicMessagesCompat struct {
	SupportsEagerToolInputStreaming *bool
	ForceAdaptiveThinking           *bool
	// SupportsLongCacheRetention overrides the long-cache TTL capability only
	// when CacheRetentionLong is requested. Nil preserves endpoint defaults.
	SupportsLongCacheRetention *bool
	AllowEmptySignature        *bool
}

func (c *AnthropicMessagesCompat) compatAPI() string { return "anthropic-messages" }

// GoogleGenerativeAICompat controls explicitly verified gateway/model features.
type GoogleGenerativeAICompat struct {
	// SupportsParametersJSONSchema opts into the parametersJsonSchema function
	// declaration field. Verify endpoint support before enabling; nil/false
	// preserve legacy parameters encoding, without full JSON Schema guarantees.
	SupportsParametersJSONSchema *bool
	// SupportsMultimodalFunctionResponses enables image parts nested inside their
	// functionResponse. Set true only for a model/endpoint supporting this protocol.
	// Nil and false preserve the legacy sibling-image wire format.
	SupportsMultimodalFunctionResponses *bool
}

func (c *GoogleGenerativeAICompat) compatAPI() string { return "google-generative-ai" }
