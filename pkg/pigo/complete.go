package pigo

import (
	"context"
	"net/http"
)

type StreamOptions struct {
	APIKey         string
	Auth           map[Provider]AuthConfig
	HTTPClient     *http.Client
	Headers        map[string]string
	MaxTokens      int
	Temperature    *float64
	Transport      Transport
	CacheRetention CacheRetention
	SessionID      string
	OnPayload      func(payload any, model Model) any
	OnResponse     func(response ProviderResponse, model Model)
	ServiceTier    string
	TimeoutMs      int
	MaxRetries     int
	MaxRetryDelay  int
	Metadata       map[string]any
	RequestContext context.Context
	Observer       Observer
}

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
