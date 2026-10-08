package pigo

// ProviderCompat describes provider-specific model compatibility metadata.
type ProviderCompat interface {
	compatAPI() string
}

// ThinkingBudgets overrides individual reasoning budgets. Zero-valued fields
// leave the provider's default budget unchanged.
type ThinkingBudgets struct {
	Minimal int
	Low     int
	Medium  int
	High    int
}

type ThinkingLevelMap map[ModelThinkingLevel]string

// SamplingParams contains JSON sampling fields for OpenAI Completions and
// Responses. These fields override the named request fields, including zero values.
// Protocol controls (model, reasoning, tools, stream, etc.) are not sampling fields
// and are rejected; use the corresponding request options instead.
type SamplingParams map[string]any

// SamplingParamsByThinkingLevel selects overrides by the effective pi thinking
// level, before that level is translated to a provider's wire value.
type SamplingParamsByThinkingLevel map[ModelThinkingLevel]SamplingParams

type HostedToolCapabilities struct {
	WebSearch  bool
	Fetch      bool
	CodeRunner bool
	Excel      bool
}

func (c HostedToolCapabilities) Supports(toolType HostedToolType) bool {
	switch toolType {
	case HostedToolTypeWebSearch:
		return c.WebSearch
	case HostedToolTypeFetch:
		return c.Fetch
	case HostedToolTypeCodeRunner:
		return c.CodeRunner
	case HostedToolTypeExcel:
		return c.Excel
	default:
		return false
	}
}

type UsageCost struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
	Total      float64
}

type ModelCostTier struct {
	InputTokensAbove int
	Rates            UsageCost
}

type Usage struct {
	Input       int
	Output      int
	CacheRead   int
	CacheWrite  int
	TotalTokens int
	Cost        UsageCost
}

type Model struct {
	ID                            string
	Name                          string
	API                           API
	Provider                      Provider
	BaseURL                       string
	Reasoning                     bool
	ThinkingLevelMap              ThinkingLevelMap
	SamplingParams                SamplingParams
	SamplingParamsByThinkingLevel SamplingParamsByThinkingLevel
	Capabilities                  ModelCapabilities
	Input                         []InputType
	HostedTools                   HostedToolCapabilities
	Cost                          UsageCost
	CostTiers                     []ModelCostTier
	ContextWindow                 int
	MaxTokens                     int
	Headers                       map[string]string
	Compat                        ProviderCompat
}
