package pigo

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func streamOpenAICodex(model Model, ctx Context, options ProviderStreamOptions) *AssistantMessageEventStream {
	options = resolveOpenAICodexProviderOptions(model, NormalizeProviderStreamOptions(model, options)).toProviderStreamOptions(model)
	stream := newAssistantMessageEventStream()
	stream.setObserver(options.Observer, model)

	response := AssistantMessage{
		API:        model.API,
		Provider:   model.Provider,
		Model:      model.ID,
		StopReason: StopReasonStop,
		Timestamp:  time.Now().UTC(),
	}

	go func() {
		requestBody := buildOpenAICodexRequest(model, ctx, options)
		payload := any(requestBody)
		if options.OnPayload != nil {
			if next := options.OnPayload(payload, model); next != nil {
				payload = next
			}
		}

		bodyBytes, err := json.Marshal(payload)
		if err != nil {
			response.StopReason = StopReasonError
			response.ErrorMessage = err.Error()
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		requestContext, cancelRequest := providerRequestContext(options.RequestContext, options.TimeoutMs)
		defer cancelRequest()
		requestContext = stream.startRequest(requestContext, payload)
		options.RequestContext = requestContext

		apiKey := options.APIKey
		if apiKey == "" {
			resolved, err := ResolveAuthorization(model.Provider, options.Auth, options.HTTPClient, requestContext)
			if err != nil {
				applyRequestError(&response, err)
				stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
				stream.finish(response)
				return
			}
			apiKey = resolved
		}
		if apiKey == "" {
			response.StopReason = StopReasonError
			response.ErrorMessage = "missing api key"
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		accountID, err := extractOpenAICodexAccountID(apiKey)
		if err != nil {
			response.StopReason = StopReasonError
			response.ErrorMessage = err.Error()
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		if err := streamOpenAICodexWithTransport(model, options, bodyBytes, apiKey, accountID, &response, stream); err != nil {
			applyRequestError(&response, err)
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}
	}()

	return stream
}

func streamSimpleOpenAICodex(model Model, ctx Context, options SimpleStreamOptions) *AssistantMessageEventStream {
	return streamOpenAICodex(model, ctx, BuildProviderStreamOptions(model, options))
}

func buildOpenAICodexRequest(model Model, ctx Context, options ProviderStreamOptions) openAIResponsesRequest {
	resolvedOptions := resolveOpenAICodexProviderOptions(model, options)

	instructions := ctx.SystemPrompt
	if strings.TrimSpace(instructions) == "" {
		instructions = "You are a helpful assistant."
	}

	requestBody := openAIResponsesRequest{
		Model:             model.ID,
		Store:             false,
		Stream:            true,
		Instructions:      instructions,
		Input:             convertOpenAIResponsesMessages(model, ctx, false),
		Tools:             convertOpenAIResponsesTools(ctx.Tools),
		ToolChoice:        resolveOpenAICodexToolChoice(resolvedOptions.ToolChoice),
		ParallelToolCalls: true,
		Include:           []string{"reasoning.encrypted_content"},
		Text: &openAIResponsesTextOptions{
			Verbosity: defaultTextVerbosity(resolvedOptions.TextVerbosity),
		},
	}

	if resolvedOptions.Temperature != nil {
		requestBody.Temperature = resolvedOptions.Temperature
	}
	if resolvedOptions.SessionID != "" {
		requestBody.PromptCacheKey = resolvedOptions.SessionID
	}
	if strings.TrimSpace(resolvedOptions.ServiceTier) != "" {
		requestBody.ServiceTier = resolvedOptions.ServiceTier
	}
	if resolvedOptions.MaxTokens > 0 {
		requestBody.MaxOutputTokens = resolvedOptions.MaxTokens
	}
	if len(resolvedOptions.Metadata) > 0 {
		requestBody.Metadata = cloneMap(resolvedOptions.Metadata)
	}
	if retention := resolveOpenAICodexCacheRetention(resolvedOptions.CacheRetention); retention != "" {
		requestBody.PromptCacheRetention = retention
	}
	if strings.TrimSpace(resolvedOptions.PreviousResponseID) != "" {
		requestBody.PreviousResponseID = resolvedOptions.PreviousResponseID
	}
	if strings.TrimSpace(resolvedOptions.Truncation) != "" {
		requestBody.Truncation = resolvedOptions.Truncation
	}

	if effort := string(resolvedOptions.Reasoning); effort != "" {
		requestBody.Reasoning = &openAIResponsesReasoningOptions{
			Effort:  effort,
			Summary: resolvedOptions.ReasoningSummary,
		}
	}

	return requestBody
}

func resolveOpenAICodexToolChoice(toolChoice string) string {
	if strings.TrimSpace(toolChoice) == "" {
		return "auto"
	}
	return strings.TrimSpace(toolChoice)
}

func resolveOpenAICodexCacheRetention(retention CacheRetention) string {
	switch retention {
	case CacheRetentionShort:
		return "24h"
	case CacheRetentionLong:
		return "7d"
	case CacheRetentionNone:
		return "0s"
	default:
		return ""
	}
}

func resolveOpenAICodexURL(baseURL string) string {
	trimmed := strings.TrimRight(baseURL, "/")
	if trimmed == "" {
		trimmed = "https://chatgpt.com/backend-api"
	}
	if strings.HasSuffix(trimmed, "/codex/responses") {
		return trimmed
	}
	if strings.HasSuffix(trimmed, "/codex") {
		return trimmed + "/responses"
	}
	return trimmed + "/codex/responses"
}

func defaultTextVerbosity(verbosity string) string {
	if strings.TrimSpace(verbosity) == "" {
		return "medium"
	}
	return verbosity
}

func defaultReasoningSummary(summary string) string {
	if strings.TrimSpace(summary) == "" {
		return "auto"
	}
	return summary
}

func extractOpenAICodexAccountID(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("failed to extract accountId from token")
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payloadBytes, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return "", fmt.Errorf("failed to extract accountId from token")
		}
	}

	var payload map[string]any
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return "", fmt.Errorf("failed to extract accountId from token")
	}

	authClaim, ok := payload["https://api.openai.com/auth"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("failed to extract accountId from token")
	}
	accountID, ok := authClaim["chatgpt_account_id"].(string)
	if !ok || strings.TrimSpace(accountID) == "" {
		return "", fmt.Errorf("failed to extract accountId from token")
	}
	return accountID, nil
}
