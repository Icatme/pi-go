package pigo

import (
	"context"
	"net/http"
)

type StreamOptions struct {
	// Common remains as a legacy compatibility snapshot. Flat fields are the
	// authoritative state and Common is rebuilt from them at conversion boundaries.
	Common CommonProviderOptions

	// Legacy flat common fields remain the direct access path for callers.
	APIKey               string
	Auth                 map[Provider]AuthConfig
	HTTPClient           *http.Client
	Headers              map[string]string
	MaxTokens            int
	Temperature          *float64
	Transport            Transport
	CacheRetention       CacheRetention
	SessionID            string
	OnPayload            func(payload any, model Model) any
	OnResponse           func(response ProviderResponse, model Model)
	ServiceTier          string
	TimeoutMs            int
	MaxRetries           int
	MaxRetryDelay        int
	Metadata             map[string]any
	RequestContext       context.Context
	Reasoning            ThinkingLevel
	ReasoningSummary     string
	TextVerbosity        string
	ThinkingBudgetTokens int
	ToolChoice           string
	PreviousResponseID   string
	Truncation           string
	ThinkingBudgets      ThinkingBudgets
	Observer             Observer
}

// Option mutates StreamOptions when constructing new option sets.
type Option func(*StreamOptions)

// NewStreamOptions builds StreamOptions from functional options.
func NewStreamOptions(options ...Option) StreamOptions {
	streamOptions := StreamOptions{}
	for _, option := range options {
		if option == nil {
			continue
		}
		option(&streamOptions)
	}
	return streamOptions.withLegacyCommonSnapshot(Model{})
}

// WithTemperature sets the shared temperature field on StreamOptions.
func WithTemperature(temperature float64) Option {
	return func(options *StreamOptions) {
		options.Temperature = &temperature
	}
}

// WithMaxTokens sets the shared max token limit on StreamOptions.
func WithMaxTokens(maxTokens int) Option {
	return func(options *StreamOptions) {
		options.MaxTokens = maxTokens
	}
}

// WithToolChoice sets the provider-specific tool choice field on StreamOptions.
func WithToolChoice(choice string) Option {
	return func(options *StreamOptions) {
		options.ToolChoice = choice
	}
}

// Deprecated: use StreamOptions with StreamOptions.providerStreamOptions instead.
type ProviderStreamOptions struct {
	APIKey               string
	Auth                 map[Provider]AuthConfig
	HTTPClient           *http.Client
	Headers              map[string]string
	MaxTokens            int
	Temperature          *float64
	Transport            Transport
	CacheRetention       CacheRetention
	SessionID            string
	OnPayload            func(payload any, model Model) any
	OnResponse           func(response ProviderResponse, model Model)
	ServiceTier          string
	TimeoutMs            int
	MaxRetries           int
	MaxRetryDelay        int
	Metadata             map[string]any
	RequestContext       context.Context
	Reasoning            ThinkingLevel
	ReasoningSummary     string
	TextVerbosity        string
	ThinkingBudgetTokens int
	ToolChoice           string
	PreviousResponseID   string
	Truncation           string
	Observer             Observer
}

// Deprecated: use StreamOptions with streamOptionsFromSimple or NewStreamOptions instead.
type SimpleStreamOptions struct {
	APIKey             string
	Auth               map[Provider]AuthConfig
	HTTPClient         *http.Client
	Headers            map[string]string
	MaxTokens          int
	Temperature        *float64
	Transport          Transport
	CacheRetention     CacheRetention
	SessionID          string
	OnPayload          func(payload any, model Model) any
	OnResponse         func(response ProviderResponse, model Model)
	ServiceTier        string
	TimeoutMs          int
	MaxRetries         int
	MaxRetryDelay      int
	Metadata           map[string]any
	RequestContext     context.Context
	Reasoning          ThinkingLevel
	ThinkingBudgets    ThinkingBudgets
	PreviousResponseID string
	Truncation         string
	Observer           Observer
}
