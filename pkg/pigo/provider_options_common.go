package pigo

import (
	"context"
	"net/http"
)

type CommonProviderOptions struct {
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
	PreviousResponseID string
	Truncation         string
	Observer           Observer
	ResponseFormat     *ResponseFormat
}

func buildCommonProviderOptions(model Model, options SimpleStreamOptions) CommonProviderOptions {
	return streamOptionsFromSimple(model, options).commonProviderOptions(model)
}

func commonProviderOptionsFromStream(options ProviderStreamOptions) CommonProviderOptions {
	return streamOptionsFromProvider(Model{}, options).commonProviderOptions(Model{})
}

func (options CommonProviderOptions) toProviderStreamOptions() ProviderStreamOptions {
	return ProviderStreamOptions{
		APIKey:             options.APIKey,
		Auth:               options.Auth,
		HTTPClient:         options.HTTPClient,
		Headers:            cloneStringMap(options.Headers),
		MaxTokens:          options.MaxTokens,
		Temperature:        options.Temperature,
		Transport:          options.Transport,
		CacheRetention:     options.CacheRetention,
		SessionID:          options.SessionID,
		OnPayload:          options.OnPayload,
		OnResponse:         options.OnResponse,
		ServiceTier:        options.ServiceTier,
		TimeoutMs:          options.TimeoutMs,
		MaxRetries:         options.MaxRetries,
		MaxRetryDelay:      options.MaxRetryDelay,
		Metadata:           cloneMap(options.Metadata),
		RequestContext:     options.RequestContext,
		PreviousResponseID: options.PreviousResponseID,
		Truncation:         options.Truncation,
		Observer:           options.Observer,
		ResponseFormat:     cloneResponseFormat(options.ResponseFormat),
	}
}

func streamOptionsFromSimple(model Model, options SimpleStreamOptions) StreamOptions {
	streamOptions := StreamOptions{
		APIKey:             options.APIKey,
		Auth:               options.Auth,
		HTTPClient:         options.HTTPClient,
		Headers:            cloneStringMap(options.Headers),
		MaxTokens:          options.MaxTokens,
		Temperature:        options.Temperature,
		Transport:          options.Transport,
		CacheRetention:     options.CacheRetention,
		SessionID:          options.SessionID,
		OnPayload:          options.OnPayload,
		OnResponse:         options.OnResponse,
		ServiceTier:        options.ServiceTier,
		TimeoutMs:          options.TimeoutMs,
		MaxRetries:         options.MaxRetries,
		MaxRetryDelay:      options.MaxRetryDelay,
		Metadata:           cloneMap(options.Metadata),
		RequestContext:     options.RequestContext,
		Reasoning:          options.Reasoning,
		PreviousResponseID: options.PreviousResponseID,
		Truncation:         options.Truncation,
		ThinkingBudgets:    options.ThinkingBudgets,
		Observer:           options.Observer,
		ResponseFormat:     cloneResponseFormat(options.ResponseFormat),
	}
	return streamOptions.withCommonSnapshot(model)
}

func streamOptionsFromProvider(model Model, options ProviderStreamOptions) StreamOptions {
	streamOptions := StreamOptions{
		APIKey:               options.APIKey,
		Auth:                 options.Auth,
		HTTPClient:           options.HTTPClient,
		Headers:              cloneStringMap(options.Headers),
		MaxTokens:            options.MaxTokens,
		Temperature:          options.Temperature,
		Transport:            options.Transport,
		CacheRetention:       options.CacheRetention,
		SessionID:            options.SessionID,
		OnPayload:            options.OnPayload,
		OnResponse:           options.OnResponse,
		ServiceTier:          options.ServiceTier,
		TimeoutMs:            options.TimeoutMs,
		MaxRetries:           options.MaxRetries,
		MaxRetryDelay:        options.MaxRetryDelay,
		Metadata:             cloneMap(options.Metadata),
		RequestContext:       options.RequestContext,
		Reasoning:            options.Reasoning,
		ReasoningSummary:     options.ReasoningSummary,
		TextVerbosity:        options.TextVerbosity,
		ThinkingBudgetTokens: options.ThinkingBudgetTokens,
		ToolChoice:           options.ToolChoice,
		PreviousResponseID:   options.PreviousResponseID,
		Truncation:           options.Truncation,
		Observer:             options.Observer,
		ResponseFormat:       cloneResponseFormat(options.ResponseFormat),
	}
	return streamOptions.withCommonSnapshot(model)
}

func (options StreamOptions) withLegacyCommonSnapshot(model Model) StreamOptions {
	options = options.normalizeLegacyCommonFallback()
	return options.withCommonSnapshot(model)
}

func (options StreamOptions) withCommonSnapshot(model Model) StreamOptions {
	options.Common = options.commonSnapshot(model)
	return options
}

func (options StreamOptions) normalizeLegacyCommonFallback() StreamOptions {
	if options.APIKey == "" {
		options.APIKey = options.Common.APIKey
	}
	if options.Auth == nil {
		options.Auth = options.Common.Auth
	}
	if options.HTTPClient == nil {
		options.HTTPClient = options.Common.HTTPClient
	}
	if len(options.Headers) == 0 {
		options.Headers = cloneStringMap(options.Common.Headers)
	} else {
		options.Headers = cloneStringMap(options.Headers)
	}
	if options.MaxTokens <= 0 {
		options.MaxTokens = options.Common.MaxTokens
	}
	if options.Temperature == nil {
		options.Temperature = options.Common.Temperature
	}
	if options.Transport == "" {
		options.Transport = options.Common.Transport
	}
	if options.CacheRetention == "" {
		options.CacheRetention = options.Common.CacheRetention
	}
	if options.SessionID == "" {
		options.SessionID = options.Common.SessionID
	}
	if options.OnPayload == nil {
		options.OnPayload = options.Common.OnPayload
	}
	if options.OnResponse == nil {
		options.OnResponse = options.Common.OnResponse
	}
	if options.ServiceTier == "" {
		options.ServiceTier = options.Common.ServiceTier
	}
	if options.TimeoutMs == 0 {
		options.TimeoutMs = options.Common.TimeoutMs
	}
	if options.MaxRetries == 0 {
		options.MaxRetries = options.Common.MaxRetries
	}
	if options.MaxRetryDelay == 0 {
		options.MaxRetryDelay = options.Common.MaxRetryDelay
	}
	if len(options.Metadata) == 0 {
		options.Metadata = cloneMap(options.Common.Metadata)
	} else {
		options.Metadata = cloneMap(options.Metadata)
	}
	if options.RequestContext == nil {
		options.RequestContext = options.Common.RequestContext
	}
	if options.PreviousResponseID == "" {
		options.PreviousResponseID = options.Common.PreviousResponseID
	}
	if options.Truncation == "" {
		options.Truncation = options.Common.Truncation
	}
	if options.Observer == nil {
		options.Observer = options.Common.Observer
	}
	if options.ResponseFormat == nil {
		options.ResponseFormat = cloneResponseFormat(options.Common.ResponseFormat)
	} else {
		options.ResponseFormat = cloneResponseFormat(options.ResponseFormat)
	}
	return options
}

func (options StreamOptions) commonSnapshot(model Model) CommonProviderOptions {
	common := CommonProviderOptions{
		APIKey:             options.APIKey,
		Auth:               options.Auth,
		HTTPClient:         options.HTTPClient,
		Headers:            cloneStringMap(options.Headers),
		MaxTokens:          options.MaxTokens,
		Temperature:        options.Temperature,
		Transport:          options.Transport,
		CacheRetention:     options.CacheRetention,
		SessionID:          options.SessionID,
		OnPayload:          options.OnPayload,
		OnResponse:         options.OnResponse,
		ServiceTier:        options.ServiceTier,
		TimeoutMs:          options.TimeoutMs,
		MaxRetries:         options.MaxRetries,
		MaxRetryDelay:      options.MaxRetryDelay,
		Metadata:           cloneMap(options.Metadata),
		RequestContext:     options.RequestContext,
		PreviousResponseID: options.PreviousResponseID,
		Truncation:         options.Truncation,
		Observer:           options.Observer,
		ResponseFormat:     cloneResponseFormat(options.ResponseFormat),
	}
	if common.MaxTokens <= 0 {
		common.MaxTokens = minInt(model.MaxTokens, 32000)
	}
	return common
}

func (options StreamOptions) commonProviderOptions(model Model) CommonProviderOptions {
	options = options.normalizeLegacyCommonFallback()
	return options.commonSnapshot(model)
}

func (options StreamOptions) providerStreamOptions(model Model) ProviderStreamOptions {
	common := options.commonProviderOptions(model)
	return ProviderStreamOptions{
		APIKey:               common.APIKey,
		Auth:                 common.Auth,
		HTTPClient:           common.HTTPClient,
		Headers:              cloneStringMap(common.Headers),
		MaxTokens:            common.MaxTokens,
		Temperature:          common.Temperature,
		Transport:            common.Transport,
		CacheRetention:       common.CacheRetention,
		SessionID:            common.SessionID,
		OnPayload:            common.OnPayload,
		OnResponse:           common.OnResponse,
		ServiceTier:          common.ServiceTier,
		TimeoutMs:            common.TimeoutMs,
		MaxRetries:           common.MaxRetries,
		MaxRetryDelay:        common.MaxRetryDelay,
		Metadata:             cloneMap(common.Metadata),
		RequestContext:       common.RequestContext,
		Reasoning:            options.Reasoning,
		ReasoningSummary:     options.ReasoningSummary,
		TextVerbosity:        options.TextVerbosity,
		ThinkingBudgetTokens: options.ThinkingBudgetTokens,
		ToolChoice:           options.ToolChoice,
		PreviousResponseID:   common.PreviousResponseID,
		Truncation:           common.Truncation,
		Observer:             common.Observer,
		ResponseFormat:       cloneResponseFormat(common.ResponseFormat),
	}
}
