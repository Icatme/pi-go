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
	}
	streamOptions.Common = streamOptions.commonProviderOptions(model)
	return streamOptions.syncLegacyFromCommon()
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
	}
	streamOptions.Common = streamOptions.commonProviderOptions(model)
	return streamOptions.syncLegacyFromCommon()
}

func (options StreamOptions) syncLegacyFromCommon() StreamOptions {
	options.APIKey = options.Common.APIKey
	options.Auth = options.Common.Auth
	options.HTTPClient = options.Common.HTTPClient
	options.Headers = cloneStringMap(options.Common.Headers)
	options.MaxTokens = options.Common.MaxTokens
	options.Temperature = options.Common.Temperature
	options.Transport = options.Common.Transport
	options.CacheRetention = options.Common.CacheRetention
	options.SessionID = options.Common.SessionID
	options.OnPayload = options.Common.OnPayload
	options.OnResponse = options.Common.OnResponse
	options.ServiceTier = options.Common.ServiceTier
	options.TimeoutMs = options.Common.TimeoutMs
	options.MaxRetries = options.Common.MaxRetries
	options.MaxRetryDelay = options.Common.MaxRetryDelay
	options.Metadata = cloneMap(options.Common.Metadata)
	options.RequestContext = options.Common.RequestContext
	options.PreviousResponseID = options.Common.PreviousResponseID
	options.Truncation = options.Common.Truncation
	options.Observer = options.Common.Observer
	return options
}

func (options StreamOptions) commonProviderOptions(model Model) CommonProviderOptions {
	common := options.Common
	if common.APIKey == "" {
		common.APIKey = options.APIKey
	}
	if common.Auth == nil {
		common.Auth = options.Auth
	}
	if common.HTTPClient == nil {
		common.HTTPClient = options.HTTPClient
	}
	if len(common.Headers) == 0 {
		common.Headers = cloneStringMap(options.Headers)
	} else {
		common.Headers = cloneStringMap(common.Headers)
	}
	if common.MaxTokens <= 0 {
		common.MaxTokens = options.MaxTokens
	}
	if common.MaxTokens <= 0 {
		common.MaxTokens = minInt(model.MaxTokens, 32000)
	}
	if common.Temperature == nil {
		common.Temperature = options.Temperature
	}
	if common.Transport == "" {
		common.Transport = options.Transport
	}
	if common.CacheRetention == "" {
		common.CacheRetention = options.CacheRetention
	}
	if common.SessionID == "" {
		common.SessionID = options.SessionID
	}
	if common.OnPayload == nil {
		common.OnPayload = options.OnPayload
	}
	if common.OnResponse == nil {
		common.OnResponse = options.OnResponse
	}
	if common.ServiceTier == "" {
		common.ServiceTier = options.ServiceTier
	}
	if common.TimeoutMs == 0 {
		common.TimeoutMs = options.TimeoutMs
	}
	if common.MaxRetries == 0 {
		common.MaxRetries = options.MaxRetries
	}
	if common.MaxRetryDelay == 0 {
		common.MaxRetryDelay = options.MaxRetryDelay
	}
	if len(common.Metadata) == 0 {
		common.Metadata = cloneMap(options.Metadata)
	} else {
		common.Metadata = cloneMap(common.Metadata)
	}
	if common.RequestContext == nil {
		common.RequestContext = options.RequestContext
	}
	if common.PreviousResponseID == "" {
		common.PreviousResponseID = options.PreviousResponseID
	}
	if common.Truncation == "" {
		common.Truncation = options.Truncation
	}
	if common.Observer == nil {
		common.Observer = options.Observer
	}
	return common
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
	}
}
