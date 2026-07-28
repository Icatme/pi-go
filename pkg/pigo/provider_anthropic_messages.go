package pigo

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

func streamAnthropicMessages(model Model, ctx Context, options ProviderStreamOptions) *AssistantMessageEventStream {
	options = NormalizeProviderStreamOptions(model, options)
	anthropicOptions := resolveAnthropicMessagesProviderOptions(model, options)
	if model.Provider == "kimi-coding" {
		anthropicOptions = resolveKimiCodingProviderOptions(model, options).AnthropicMessagesProviderOptions
	}
	hostedTools := supportedHostedTools(model, ctx.HostedTools)
	if len(hostedTools) > 0 {
		ctx.HostedTools = hostedTools
		return streamAnthropicMessagesWithHostedTools(model, ctx, anthropicOptions)
	}

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
		requestContext, cancel := providerRequestContext(anthropicOptions.RequestContext, anthropicOptions.TimeoutMs)
		defer cancel()
		anthropicOptions.RequestContext = requestContext

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
		isOAuth := false
		if apiKey == "" {
			apiKey = ResolveAPIKey(model.Provider, options.Auth)
		}
		if model.Provider == "anthropic" {
			if authConfig, ok := options.Auth[model.Provider]; ok && authConfig.Type == AuthTypeOAuth {
				isOAuth = true
			}
			if isAnthropicOAuthToken(apiKey) {
				isOAuth = true
			}
		}
		if apiKey == "" {
			response.StopReason = StopReasonError
			response.ErrorMessage = "missing api key"
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		requestBody := buildAnthropicRequest(model, ctx, anthropicOptions, isOAuth, true)
		payload := any(requestBody)
		if anthropicOptions.OnPayload != nil {
			if next := anthropicOptions.OnPayload(payload, model); next != nil {
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
		requestContext = stream.startRequest(requestContext, payload)
		anthropicOptions.RequestContext = requestContext

		request, err := http.NewRequestWithContext(
			requestContext,
			http.MethodPost,
			strings.TrimRight(model.BaseURL, "/")+"/v1/messages",
			bytes.NewReader(bodyBytes),
		)
		if err != nil {
			applyRequestError(&response, err)
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		request.Header.Set("content-type", "application/json")
		request.Header.Set("accept", "text/event-stream")
		request.Header.Set("anthropic-version", "2023-06-01")
		if model.Provider == "anthropic" {
			request.Header.Set("anthropic-beta", buildAnthropicBetaHeader(model, isOAuth))
			if isOAuth {
				request.Header.Set("authorization", "Bearer "+apiKey)
				request.Header.Set("user-agent", "claude-cli/"+anthropicClaudeCodeVersion)
				request.Header.Set("x-app", "cli")
			} else {
				request.Header.Set("x-api-key", apiKey)
			}
		} else {
			request.Header.Set("x-api-key", apiKey)
		}
		for key, value := range anthropicOptions.Headers {
			request.Header.Set(key, value)
		}

		httpClient := anthropicOptions.HTTPClient
		if httpClient == nil {
			httpClient = http.DefaultClient
		}

		httpResponse, err := httpClient.Do(request)
		if err != nil {
			applyRequestError(&response, err)
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}
		defer httpResponse.Body.Close()
		notifyAnthropicResponse(anthropicOptions.OnResponse, model, httpResponse)

		if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
			body, _ := io.ReadAll(httpResponse.Body)
			response.StopReason = StopReasonError
			response.ErrorMessage = parseAnthropicError(body, httpResponse.Status)
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		stream.push(AssistantMessageEvent{
			Type:    AssistantMessageEventStart,
			Partial: response,
		})

		terminalSeen := false
		states := map[int]*anthropicStreamingBlockState{}
		if err := readSSEStream(httpResponse.Body, func(_ string, data string) (bool, error) {
			done, err := processAnthropicStreamEvent(data, model, &response, stream, states, isOAuth, ctx.Tools, nil, true)
			if done {
				terminalSeen = true
				return true, nil
			}
			return false, err
		}); err != nil {
			applyRequestError(&response, err)
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		if !terminalSeen {
			applyRequestError(&response, errAnthropicStreamMissingTerminal)
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
		}
	}()

	return stream
}

func streamSimpleAnthropicMessages(model Model, ctx Context, options SimpleStreamOptions) *AssistantMessageEventStream {
	return streamAnthropicMessages(model, ctx, BuildProviderStreamOptions(model, options))
}
