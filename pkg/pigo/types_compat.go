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
	SendSessionIdHeader        *bool
	SupportsLongCacheRetention *bool
}

func (c *OpenAIResponsesCompat) compatAPI() string { return "openai-responses" }

type AnthropicMessagesCompat struct {
	SupportsEagerToolInputStreaming *bool
	SupportsLongCacheRetention      *bool
}

func (c *AnthropicMessagesCompat) compatAPI() string { return "anthropic-messages" }
