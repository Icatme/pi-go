package pigo

import (
	"context"
	"net/http"
)

type CommonProviderOptions struct {
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
	ServiceTier    string
	MaxRetryDelay  int
	Metadata       map[string]any
	RequestContext context.Context
}

func buildCommonProviderOptions(model Model, options SimpleStreamOptions) CommonProviderOptions {
	maxTokens := options.MaxTokens
	if maxTokens <= 0 {
		maxTokens = minInt(model.MaxTokens, 32000)
	}

	return CommonProviderOptions{
		APIKey:         options.APIKey,
		Auth:           options.Auth,
		HTTPClient:     options.HTTPClient,
		Headers:        cloneStringMap(options.Headers),
		MaxTokens:      maxTokens,
		Temperature:    options.Temperature,
		Transport:      options.Transport,
		CacheRetention: options.CacheRetention,
		SessionID:      options.SessionID,
		OnPayload:      options.OnPayload,
		ServiceTier:    options.ServiceTier,
		MaxRetryDelay:  options.MaxRetryDelay,
		Metadata:       cloneMap(options.Metadata),
		RequestContext: options.RequestContext,
	}
}

func commonProviderOptionsFromStream(options ProviderStreamOptions) CommonProviderOptions {
	return CommonProviderOptions{
		APIKey:         options.APIKey,
		Auth:           options.Auth,
		HTTPClient:     options.HTTPClient,
		Headers:        cloneStringMap(options.Headers),
		MaxTokens:      options.MaxTokens,
		Temperature:    options.Temperature,
		Transport:      options.Transport,
		CacheRetention: options.CacheRetention,
		SessionID:      options.SessionID,
		OnPayload:      options.OnPayload,
		ServiceTier:    options.ServiceTier,
		MaxRetryDelay:  options.MaxRetryDelay,
		Metadata:       cloneMap(options.Metadata),
		RequestContext: options.RequestContext,
	}
}

func (options CommonProviderOptions) toProviderStreamOptions() ProviderStreamOptions {
	return ProviderStreamOptions{
		APIKey:         options.APIKey,
		Auth:           options.Auth,
		HTTPClient:     options.HTTPClient,
		Headers:        cloneStringMap(options.Headers),
		MaxTokens:      options.MaxTokens,
		Temperature:    options.Temperature,
		Transport:      options.Transport,
		CacheRetention: options.CacheRetention,
		SessionID:      options.SessionID,
		OnPayload:      options.OnPayload,
		ServiceTier:    options.ServiceTier,
		MaxRetryDelay:  options.MaxRetryDelay,
		Metadata:       cloneMap(options.Metadata),
		RequestContext: options.RequestContext,
	}
}
