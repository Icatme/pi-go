package pigo

import (
	"context"
	"net/http"
)

type CompleteOptions struct {
	APIKey               string
	Auth                 map[Provider]AuthConfig
	HTTPClient           *http.Client
	Headers              map[string]string
	MaxTokens            int
	Temperature          *float64
	SessionID            string
	CacheRetention       CacheRetention
	Reasoning            ThinkingLevel
	ReasoningSummary     string
	TextVerbosity        string
	ThinkingBudgetTokens int
	ToolChoice           string
	OnPayload            func(payload any, model Model) any
	RequestContext       context.Context
}
