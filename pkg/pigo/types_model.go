package pigo

// ProviderCompat describes provider-specific model compatibility metadata.
type ProviderCompat interface {
	compatAPI() string
}

type ThinkingBudgets struct {
	Minimal int
	Low     int
	Medium  int
	High    int
}

type ThinkingLevelMap map[ModelThinkingLevel]string

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

type Usage struct {
	Input       int
	Output      int
	CacheRead   int
	CacheWrite  int
	TotalTokens int
	Cost        UsageCost
}

type Model struct {
	ID               string
	Name             string
	API              API
	Provider         Provider
	BaseURL          string
	Reasoning        bool
	ThinkingLevelMap ThinkingLevelMap
	Input            []InputType
	HostedTools      HostedToolCapabilities
	Cost             UsageCost
	ContextWindow    int
	MaxTokens        int
	Headers          map[string]string
	Compat           ProviderCompat
}
