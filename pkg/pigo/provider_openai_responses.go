package pigo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	openai "github.com/openai/openai-go/v3"
	openaioption "github.com/openai/openai-go/v3/option"
)

func streamOpenAIResponses(model Model, ctx Context, options ProviderStreamOptions) *AssistantMessageEventStream {
	options = resolveOpenAIResponsesProviderOptions(model, NormalizeProviderStreamOptions(model, options)).toProviderStreamOptions(model)
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
		apiKey := options.APIKey
		if apiKey == "" {
			resolved, err := ResolveAuthorization(model.Provider, options.Auth, options.HTTPClient, options.RequestContext)
			if err != nil {
				response.StopReason = StopReasonError
				response.ErrorMessage = err.Error()
				stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
				stream.finish(response)
				return
			}
			apiKey = resolved
		}
		if apiKey == "" {
			apiKey = ResolveAPIKey(model.Provider, options.Auth)
		}
		if apiKey == "" {
			apiKey = GetEnvAPIKey(model.Provider)
		}
		if apiKey == "" {
			response.StopReason = StopReasonError
			response.ErrorMessage = "missing api key"
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		requestBody := buildOpenAIResponsesRequest(model, ctx, options)
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

		requestContext := options.RequestContext
		if requestContext == nil {
			requestContext = context.Background()
		}

		httpClient := options.HTTPClient
		if httpClient == nil {
			httpClient = http.DefaultClient
		}

		if err := streamOpenAIResponsesSSE(model, requestContext, httpClient, options, bodyBytes, apiKey, &response, stream); err != nil {
			applyRequestError(&response, err)
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}
	}()

	return stream
}

func streamSimpleOpenAIResponses(model Model, ctx Context, options SimpleStreamOptions) *AssistantMessageEventStream {
	return streamOpenAIResponses(model, ctx, BuildProviderStreamOptions(model, options))
}

func buildOpenAIResponsesRequest(model Model, ctx Context, options ProviderStreamOptions) openAIResponsesRequest {
	resolvedOptions := resolveOpenAIResponsesProviderOptions(model, options)

	requestBody := openAIResponsesRequest{
		Model:             model.ID,
		Store:             false,
		Stream:            true,
		Input:             convertOpenAIResponsesMessages(model, ctx, true),
		Tools:             convertOpenAIResponsesTools(ctx.Tools),
		ToolChoice:        resolveOpenAIResponsesToolChoice(resolvedOptions.ToolChoice),
		ParallelToolCalls: true,
		Include:           []string{"reasoning.encrypted_content"},
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
	if retention := resolveOpenAIResponsesCacheRetention(resolvedOptions.CacheRetention); retention != "" {
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

func resolveOpenAIResponsesToolChoice(toolChoice string) string {
	if strings.TrimSpace(toolChoice) == "" {
		return "auto"
	}
	return strings.TrimSpace(toolChoice)
}

func resolveOpenAIResponsesCacheRetention(retention CacheRetention) string {
	switch retention {
	case CacheRetentionLong:
		return "24h"
	default:
		return ""
	}
}

func resolveOpenAIResponsesURL(baseURL string) string {
	trimmed := strings.TrimRight(baseURL, "/")
	if trimmed == "" {
		trimmed = "https://api.openai.com/v1"
	}
	if strings.HasSuffix(trimmed, "/responses") {
		return trimmed
	}
	if strings.HasSuffix(trimmed, "/v1") {
		return trimmed + "/responses"
	}
	if parsed, err := url.Parse(trimmed); err == nil && (parsed.Path == "" || parsed.Path == "/") {
		return trimmed + "/v1/responses"
	}
	return trimmed + "/responses"
}

func streamOpenAIResponsesSSE(
	model Model,
	requestContext context.Context,
	httpClient *http.Client,
	options ProviderStreamOptions,
	bodyBytes []byte,
	apiKey string,
	response *AssistantMessage,
	stream *AssistantMessageEventStream,
) error {
	clientOptions := []openaioption.RequestOption{
		openaioption.WithAPIKey(apiKey),
		openaioption.WithBaseURL(resolveOpenAIResponsesSDKBaseURL(model.BaseURL)),
		openaioption.WithMaxRetries(maxInt(options.MaxRetries, 3)),
	}
	if httpClient != nil {
		clientOptions = append(clientOptions, openaioption.WithHTTPClient(httpClient))
	}

	sdkClient := openai.NewClient(clientOptions...)

	payload := json.RawMessage(bodyBytes)
	requestOptions := []openaioption.RequestOption{
		openaioption.WithHeader("accept", "text/event-stream"),
	}
	if options.SessionID != "" {
		requestOptions = append(requestOptions,
			openaioption.WithHeader("session_id", options.SessionID),
			openaioption.WithHeader("x-client-request-id", options.SessionID),
		)
	}
	for key, value := range options.Headers {
		requestOptions = append(requestOptions, openaioption.WithHeader(key, value))
	}

	var httpResponse *http.Response
	err := sdkClient.Post(requestContext, "responses", payload, &httpResponse, requestOptions...)
	if err != nil {
		return err
	}
	if httpResponse == nil {
		return fmt.Errorf("missing sdk response")
	}
	defer httpResponse.Body.Close()

	if options.OnResponse != nil {
		headers := make(map[string]string)
		for key, values := range httpResponse.Header {
			if len(values) > 0 {
				headers[key] = values[0]
			}
		}
		options.OnResponse(ProviderResponse{Status: httpResponse.StatusCode, Headers: headers}, model)
	}

	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		body, readErr := io.ReadAll(httpResponse.Body)
		if readErr != nil {
			return readErr
		}
		return errors.New(parseOpenAIResponsesError(body, httpResponse.Status))
	}

	state := openAIResponsesStreamingState{
		CurrentTextIndex:     -1,
		CurrentThinkingIndex: -1,
		CurrentToolIndex:     -1,
		FinalizedItemKeys:    map[string]bool{},
	}
	terminalSeen := false
	stream.push(AssistantMessageEvent{Type: AssistantMessageEventStart, Partial: *response})
	err = readSSEStream(httpResponse.Body, func(_ string, data string) (bool, error) {
		done, eventErr := processOpenAIResponsesStreamEvent(data, model, response, stream, &state, options.ServiceTier)
		if done {
			terminalSeen = true
		}
		return done, eventErr
	})
	if err != nil {
		return err
	}
	if !terminalSeen {
		return fmt.Errorf("missing terminal sse event")
	}
	return nil
}

func resolveOpenAIResponsesSDKBaseURL(baseURL string) string {
	resolvedURL := resolveOpenAIResponsesURL(baseURL)
	if strings.HasSuffix(resolvedURL, "/responses") {
		return strings.TrimSuffix(resolvedURL, "/responses")
	}
	return resolvedURL
}

func buildOpenAIResponsesSSEHeaders(options ProviderStreamOptions, apiKey string) map[string]string {
	headers := map[string]string{
		"content-type":  "application/json",
		"accept":        "text/event-stream",
		"authorization": "Bearer " + apiKey,
	}
	if options.SessionID != "" {
		headers["session_id"] = options.SessionID
		headers["x-client-request-id"] = options.SessionID
	}
	for key, value := range options.Headers {
		headers[key] = value
	}
	return headers
}

func waitOpenAIResponsesRetryDelay(ctx context.Context, maxRetryDelayMS int, attempt int, baseDelay time.Duration) error {
	delay := baseDelay * time.Duration(1<<attempt)
	if maxRetryDelayMS > 0 {
		maxDelay := time.Duration(maxRetryDelayMS) * time.Millisecond
		if delay > maxDelay {
			delay = maxDelay
		}
	}
	if delay <= 0 {
		return ctx.Err()
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
