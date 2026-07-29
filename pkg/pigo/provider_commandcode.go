package pigo

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

const (
	commandCodeDefaultBaseURL       = "https://api.commandcode.ai"
	commandCodeCLIVersion           = "0.29.0"
	commandCodeDefaultMaxTokens     = 64_000
	commandCodeDefaultMaxRetryDelay = 60_000
	commandCodeBaseRetryDelay       = 500
)

// This transport is a native Go port of pi-commandcode-provider v0.4.2's
// commandcode-custom wire protocol. It deliberately excludes that extension's
// credential-file reads and browser login; callers own credential persistence.

type commandCodeRequest struct {
	Config   commandCodeConfig `json:"config"`
	Memory   any               `json:"memory"`
	Taste    any               `json:"taste"`
	Skills   any               `json:"skills"`
	Params   commandCodeParams `json:"params"`
	ThreadID string            `json:"threadId"`
}

type commandCodeConfig struct {
	WorkingDir    string `json:"workingDir"`
	Date          string `json:"date"`
	Environment   string `json:"environment"`
	Structure     []any  `json:"structure"`
	IsGitRepo     bool   `json:"isGitRepo"`
	CurrentBranch string `json:"currentBranch"`
	MainBranch    string `json:"mainBranch"`
	GitStatus     string `json:"gitStatus"`
	RecentCommits []any  `json:"recentCommits"`
}

type commandCodeParams struct {
	Model       string  `json:"model"`
	Messages    []any   `json:"messages"`
	Tools       []any   `json:"tools"`
	System      string  `json:"system"`
	MaxTokens   int     `json:"max_tokens"`
	Temperature float64 `json:"temperature"`
	Stream      bool    `json:"stream"`
}

type commandCodeStreamState struct {
	textIndex     int
	textStarted   bool
	text          strings.Builder
	thinkingIndex int
	thinkingOpen  bool
	thinking      strings.Builder
	finished      bool
}

func newCommandCodeAPIModule() APIModule {
	return APIModule{
		API:          "commandcode-custom",
		Stream:       streamCommandCode,
		StreamSimple: streamSimpleCommandCode,
	}
}

func buildCommandCodeProviderStreamOptions(model Model, options SimpleStreamOptions) ProviderStreamOptions {
	streamOptions := streamOptionsFromSimple(model, options)
	if options.MaxTokens <= 0 {
		streamOptions.MaxTokens = model.MaxTokens
	}
	return streamOptions.providerStreamOptions(model)
}

func normalizeCommandCodeProviderStreamOptions(model Model, options ProviderStreamOptions) ProviderStreamOptions {
	streamOptions := streamOptionsFromProvider(model, options)
	if options.MaxTokens <= 0 {
		streamOptions.MaxTokens = model.MaxTokens
	}
	return streamOptions.providerStreamOptions(model)
}

func streamSimpleCommandCode(model Model, ctx Context, options SimpleStreamOptions) *AssistantMessageEventStream {
	return streamCommandCode(model, ctx, buildCommandCodeProviderStreamOptions(model, options))
}

func streamCommandCode(model Model, ctx Context, options ProviderStreamOptions) *AssistantMessageEventStream {
	options = normalizeCommandCodeProviderStreamOptions(model, options)
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
			apiKey = ResolveAPIKey(model.Provider, options.Auth)
		}
		if apiKey == "" {
			apiKey = GetEnvAPIKey(model.Provider)
		}
		if apiKey == "" {
			response.StopReason = StopReasonError
			response.ErrorMessage = "missing Command Code API key"
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		payload, workingDir, err := buildCommandCodeRequest(model, ctx, options)
		if err != nil {
			response.StopReason = StopReasonError
			response.ErrorMessage = err.Error()
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}
		var requestPayload any = payload
		if options.OnPayload != nil {
			if next := options.OnPayload(requestPayload, model); next != nil {
				requestPayload = next
			}
		}
		body, err := json.Marshal(requestPayload)
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
		requestContext = stream.startRequest(requestContext, requestPayload)
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventStart, Partial: response})

		httpClient := options.HTTPClient
		if httpClient == nil {
			httpClient = http.DefaultClient
		}
		headers := commandCodeRequestHeaders(options.Headers, apiKey, workingDir)
		state, err := executeCommandCodeRequest(requestContext, httpClient, model, options, headers, body, &response, stream)
		if err != nil {
			applyRequestError(&response, err)
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: response.StopReason, Error: response})
			stream.finish(response)
			return
		}

		finishCommandCodeBlocks(&response, stream, state)
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventDone, Reason: response.StopReason, Message: response})
		stream.finish(response)
	}()

	return stream
}

func buildCommandCodeRequest(model Model, ctx Context, options ProviderStreamOptions) (commandCodeRequest, string, error) {
	workingDir, err := os.Getwd()
	if err != nil {
		return commandCodeRequest{}, "", fmt.Errorf("resolve working directory: %w", err)
	}
	threadID, err := newCommandCodeThreadID()
	if err != nil {
		return commandCodeRequest{}, "", fmt.Errorf("create Command Code thread id: %w", err)
	}
	maxTokens := options.MaxTokens
	if maxTokens <= 0 {
		maxTokens = model.MaxTokens
	}
	maxTokens = minInt(maxTokens, model.MaxTokens)
	maxTokens = minInt(maxTokens, commandCodeDefaultMaxTokens)

	return commandCodeRequest{
		Config: commandCodeConfig{
			WorkingDir:    workingDir,
			Date:          time.Now().UTC().Format("2006-01-02"),
			Environment:   fmt.Sprintf("%s-%s, Go %s", runtime.GOOS, runtime.GOARCH, runtime.Version()),
			Structure:     []any{},
			IsGitRepo:     false,
			CurrentBranch: "",
			MainBranch:    "",
			GitStatus:     "",
			RecentCommits: []any{},
		},
		Memory: nil,
		Taste:  nil,
		Skills: nil,
		Params: commandCodeParams{
			Model:       model.ID,
			Messages:    commandCodeMessages(ctx.Messages),
			Tools:       commandCodeTools(ctx.Tools),
			System:      ctx.SystemPrompt,
			MaxTokens:   maxTokens,
			Temperature: 0.3,
			Stream:      true,
		},
		ThreadID: threadID,
	}, workingDir, nil
}

func commandCodeRequestHeaders(overrides map[string]string, apiKey, workingDir string) map[string]string {
	return mergeRequestHeaders(map[string]string{
		"Accept":                 "*/*",
		"Accept-Encoding":        "gzip, deflate",
		"Accept-Language":        "*",
		"Authorization":          "Bearer " + apiKey,
		"Content-Type":           "application/json",
		"Sec-Fetch-Mode":         "cors",
		"User-Agent":             "node",
		"x-cli-environment":      "production",
		"x-co-flag":              "false",
		"x-command-code-version": commandCodeCLIVersion,
		"x-project-slug":         commandCodeProjectSlug(workingDir),
		"x-taste-learning":       "true",
	}, overrides)
}

func executeCommandCodeRequest(
	ctx context.Context,
	client *http.Client,
	model Model,
	options ProviderStreamOptions,
	headers map[string]string,
	body []byte,
	response *AssistantMessage,
	stream *AssistantMessageEventStream,
) (*commandCodeStreamState, error) {
	maxRetries := maxInt(0, options.MaxRetries)
	maxRetryDelay := options.MaxRetryDelay
	if maxRetryDelay <= 0 {
		maxRetryDelay = commandCodeDefaultMaxRetryDelay
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		attemptContext := ctx
		cancel := func() {}
		if options.TimeoutMs > 0 {
			attemptContext, cancel = context.WithTimeout(ctx, time.Duration(options.TimeoutMs)*time.Millisecond)
		}

		request, err := http.NewRequestWithContext(attemptContext, http.MethodPost, resolveCommandCodeGenerateURL(model.BaseURL), bytes.NewReader(body))
		if err != nil {
			cancel()
			return nil, err
		}
		for key, value := range headers {
			if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
				request.Header.Set(key, value)
			}
		}

		httpResponse, err := client.Do(request)
		if err != nil {
			attemptErr := attemptContext.Err()
			cancel()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.Is(attemptErr, context.DeadlineExceeded) && attempt < maxRetries {
				continue
			}
			if attemptErr != nil {
				return nil, attemptErr
			}
			return nil, err
		}

		if isCommandCodeRetryableStatus(httpResponse.StatusCode) {
			delay, retryErr := commandCodeRetryDelay(attempt, httpResponse.Header.Get("Retry-After"), maxRetryDelay)
			if retryErr != nil {
				_ = httpResponse.Body.Close()
				cancel()
				return nil, retryErr
			}
			if attempt < maxRetries {
				_, _ = io.Copy(io.Discard, httpResponse.Body)
				_ = httpResponse.Body.Close()
				cancel()
				if err := waitCommandCodeRetry(ctx, delay); err != nil {
					return nil, err
				}
				continue
			}
		}

		if options.OnResponse != nil {
			options.OnResponse(providerResponseFromHTTPResponse(httpResponse), model)
		}
		if err := decodeCommandCodeResponseBody(httpResponse); err != nil {
			_ = httpResponse.Body.Close()
			cancel()
			return nil, err
		}
		if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
			errorBody, _ := io.ReadAll(io.LimitReader(httpResponse.Body, 500))
			_ = httpResponse.Body.Close()
			cancel()
			return nil, fmt.Errorf("Command Code API error %d: %s", httpResponse.StatusCode, strings.TrimSpace(string(errorBody)))
		}

		state := &commandCodeStreamState{textIndex: -1, thinkingIndex: -1}
		streamErr := readCommandCodeStream(httpResponse.Body, model, response, stream, state)
		_ = httpResponse.Body.Close()
		attemptErr := attemptContext.Err()
		cancel()
		if streamErr == nil && !state.finished {
			streamErr = errors.New("Command Code stream ended before finish event")
		}
		if streamErr == nil {
			return state, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if len(response.Content) == 0 && attempt < maxRetries {
			resetCommandCodeResponse(response)
			if !errors.Is(attemptErr, context.DeadlineExceeded) {
				delay, retryErr := commandCodeRetryDelay(attempt, "", maxRetryDelay)
				if retryErr != nil {
					return nil, retryErr
				}
				if err := waitCommandCodeRetry(ctx, delay); err != nil {
					return nil, err
				}
			}
			continue
		}
		if attemptErr != nil {
			return nil, attemptErr
		}
		return nil, streamErr
	}

	return nil, errors.New("Command Code request failed after retries")
}

func readCommandCodeStream(reader io.Reader, model Model, response *AssistantMessage, stream *AssistantMessageEventStream, state *commandCodeStreamState) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		value, ok := parseCommandCodeStreamLine(scanner.Text())
		if !ok {
			continue
		}
		finished, err := processCommandCodeStreamEvent(value, model, response, stream, state)
		if err != nil {
			return err
		}
		if finished {
			return nil
		}
	}
	return scanner.Err()
}

func parseCommandCodeStreamLine(line string) (map[string]any, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, ":") || strings.HasPrefix(trimmed, "event:") {
		return nil, false
	}
	if strings.HasPrefix(trimmed, "data:") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	}
	if trimmed == "" || trimmed == "[DONE]" {
		return nil, false
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(trimmed), &event); err != nil || event == nil {
		return nil, false
	}
	return event, true
}

func processCommandCodeStreamEvent(event map[string]any, model Model, response *AssistantMessage, stream *AssistantMessageEventStream, state *commandCodeStreamState) (bool, error) {
	switch anyString(event["type"]) {
	case "text-delta":
		endCommandCodeThinking(response, stream, state)
		if !state.textStarted {
			state.textStarted = true
			state.textIndex = len(response.Content)
			response.Content = append(response.Content, TextContent{})
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextStart, ContentIndex: state.textIndex, Partial: *response})
		}
		delta := anyString(event["text"])
		state.text.WriteString(delta)
		response.Content[state.textIndex] = TextContent{Text: state.text.String()}
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextDelta, ContentIndex: state.textIndex, Delta: delta, Partial: *response})
	case "reasoning-start":
		endCommandCodeText(response, stream, state)
	case "reasoning-delta":
		endCommandCodeText(response, stream, state)
		delta := anyString(event["text"])
		if !state.thinkingOpen {
			state.thinkingOpen = true
			state.thinkingIndex = len(response.Content)
			response.Content = append(response.Content, ThinkingContent{})
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventThinkingStart, ContentIndex: state.thinkingIndex, Partial: *response})
		}
		state.thinking.WriteString(delta)
		response.Content[state.thinkingIndex] = ThinkingContent{Thinking: state.thinking.String()}
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventThinkingDelta, ContentIndex: state.thinkingIndex, Delta: delta, Partial: *response})
	case "reasoning-end":
		endCommandCodeThinking(response, stream, state)
	case "tool-result":
		return false, nil
	case "tool-call":
		endCommandCodeText(response, stream, state)
		endCommandCodeThinking(response, stream, state)
		arguments := commandCodeRecord(event["input"])
		if len(arguments) == 0 {
			arguments = commandCodeRecord(event["args"])
		}
		if len(arguments) == 0 {
			arguments = commandCodeRecord(event["arguments"])
		}
		toolCall := ToolCall{ID: anyString(event["toolCallId"]), Name: anyString(event["toolName"]), Arguments: arguments}
		index := len(response.Content)
		response.Content = append(response.Content, toolCall)
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventToolCallStart, ContentIndex: index, ToolCall: toolCall, Partial: *response})
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventToolCallEnd, ContentIndex: index, ToolCall: toolCall, Partial: *response})
	case "finish":
		applyCommandCodeUsage(event, model, response)
		response.StopReason = mapCommandCodeFinishReason(anyString(event["finishReason"]))
		state.finished = true
		return true, nil
	case "error":
		message := "Stream error"
		if errorRecord := commandCodeRecord(event["error"]); anyString(errorRecord["message"]) != "" {
			message = anyString(errorRecord["message"])
		} else if text := anyString(event["error"]); text != "" {
			message = text
		}
		return false, errors.New(message)
	}
	return false, nil
}

func finishCommandCodeBlocks(response *AssistantMessage, stream *AssistantMessageEventStream, state *commandCodeStreamState) {
	endCommandCodeText(response, stream, state)
	endCommandCodeThinking(response, stream, state)
}

func endCommandCodeText(response *AssistantMessage, stream *AssistantMessageEventStream, state *commandCodeStreamState) {
	if !state.textStarted {
		return
	}
	stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextEnd, ContentIndex: state.textIndex, Content: state.text.String(), Partial: *response})
	state.textStarted = false
	state.textIndex = -1
	state.text.Reset()
}

func endCommandCodeThinking(response *AssistantMessage, stream *AssistantMessageEventStream, state *commandCodeStreamState) {
	if !state.thinkingOpen {
		return
	}
	stream.push(AssistantMessageEvent{Type: AssistantMessageEventThinkingEnd, ContentIndex: state.thinkingIndex, Content: state.thinking.String(), Partial: *response})
	state.thinkingOpen = false
	state.thinkingIndex = -1
	state.thinking.Reset()
}

func applyCommandCodeUsage(event map[string]any, model Model, response *AssistantMessage) {
	totalUsage := commandCodeRecord(event["totalUsage"])
	if len(totalUsage) == 0 {
		return
	}
	details := commandCodeRecord(totalUsage["inputTokenDetails"])
	response.Usage = Usage{
		Input:      commandCodeInt(totalUsage["inputTokens"]),
		Output:     commandCodeInt(totalUsage["outputTokens"]),
		CacheRead:  commandCodeInt(details["cacheReadTokens"]),
		CacheWrite: commandCodeInt(details["cacheWriteTokens"]),
	}
	response.Usage.TotalTokens = response.Usage.Input + response.Usage.Output + response.Usage.CacheRead + response.Usage.CacheWrite
	response.Usage.Cost = calculateCommandCodeCost(model, response.Usage)
}

func calculateCommandCodeCost(model Model, usage Usage) UsageCost {
	if tier, ok := commandCodeLongContextCosts[model.ID]; ok && usage.Input+usage.CacheRead > tier.Threshold {
		model.Cost = tier.Cost
	}
	return CalculateCost(model, usage)
}

func resetCommandCodeResponse(response *AssistantMessage) {
	response.Content = nil
	response.Usage = Usage{}
	response.StopReason = StopReasonStop
	response.ErrorMessage = ""
}

func commandCodeMessages(messages []Message) []any {
	paired := commandCodePairedToolCallIDs(messages)
	result := make([]any, 0, len(messages))
	for _, message := range messages {
		switch typed := message.(type) {
		case UserMessage:
			result = append(result, map[string]any{"role": "user", "content": commandCodeUserContent(typed.Content)})
		case AssistantMessage:
			parts := make([]any, 0, len(typed.Content))
			for _, block := range typed.Content {
				switch content := block.(type) {
				case TextContent:
					parts = append(parts, map[string]any{"type": "text", "text": content.Text})
				case ThinkingContent:
					parts = append(parts, map[string]any{"type": "reasoning", "text": content.Thinking})
				case ToolCall:
					if paired[content.ID] {
						arguments := cloneMap(content.Arguments)
						if arguments == nil {
							arguments = map[string]any{}
						}
						parts = append(parts, map[string]any{"type": "tool-call", "toolCallId": content.ID, "toolName": content.Name, "input": arguments})
					}
				}
			}
			if len(parts) > 0 {
				result = append(result, map[string]any{"role": "assistant", "content": parts})
			}
		case ToolResultMessage:
			if typed.ToolCallID == "" || !paired[typed.ToolCallID] {
				continue
			}
			outputType := "text"
			if typed.IsError {
				outputType = "error-text"
			}
			result = append(result, map[string]any{
				"role": "tool",
				"content": []any{map[string]any{
					"type":       "tool-result",
					"toolCallId": typed.ToolCallID,
					"toolName":   typed.ToolName,
					"output":     map[string]any{"type": outputType, "value": commandCodeTextContent(typed.Content)},
				}},
			})
		}
	}
	return result
}

func commandCodePairedToolCallIDs(messages []Message) map[string]bool {
	calls := map[string]bool{}
	results := map[string]bool{}
	for _, message := range messages {
		switch typed := message.(type) {
		case AssistantMessage:
			for _, block := range typed.Content {
				if toolCall, ok := block.(ToolCall); ok && toolCall.ID != "" {
					calls[toolCall.ID] = true
				}
			}
		case ToolResultMessage:
			if typed.ToolCallID != "" {
				results[typed.ToolCallID] = true
			}
		}
	}
	paired := map[string]bool{}
	for id := range calls {
		paired[id] = results[id]
	}
	return paired
}

func commandCodeUserContent(content any) any {
	switch typed := content.(type) {
	case []ContentBlock:
		parts := make([]any, 0, len(typed))
		for _, block := range typed {
			switch value := block.(type) {
			case TextContent:
				parts = append(parts, map[string]any{"type": "text", "text": value.Text})
			case ImageContent:
				parts = append(parts, map[string]any{"type": "image", "data": value.Data, "mimeType": value.MIMEType})
			}
		}
		return parts
	default:
		return cloneAny(content)
	}
}

func commandCodeTextContent(blocks []ContentBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if text, ok := block.(TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func commandCodeTools(tools []Tool) []any {
	result := make([]any, 0, len(tools))
	for _, tool := range tools {
		parameters := commandCodeJSONSchema(tool.Parameters)
		result = append(result, map[string]any{
			"type":         "function",
			"name":         tool.Name,
			"description":  tool.Description,
			"input_schema": parameters,
		})
	}
	return result
}

func commandCodeJSONSchema(schema any) any {
	record := commandCodeRecord(schema)
	if len(record) == 0 {
		return map[string]any{}
	}
	if _, hasKind := record["kind"]; !hasKind {
		return record
	}
	if enum := commandCodeAnySlice(record["enum"]); len(enum) > 0 {
		typeName := ""
		if len(enum) > 0 {
			switch enum[0].(type) {
			case string:
				typeName = "string"
			case float64, float32, int, int32, int64:
				typeName = "number"
			case bool:
				typeName = "boolean"
			}
		}
		return map[string]any{"type": typeName, "enum": cloneAny(enum)}
	}
	kind := anyString(record["kind"])
	if kind == "" {
		kind = anyString(record["type"])
	}
	switch strings.ToLower(kind) {
	case "string", "number", "integer", "boolean", "null":
		return map[string]any{"type": strings.ToLower(kind)}
	case "object":
		properties := map[string]any{}
		required := commandCodeStringSlice(record["required"])
		optional := commandCodeStringSet(record["optional"])
		if source := commandCodeRecord(record["properties"]); len(source) > 0 {
			for key, value := range source {
				properties[key] = commandCodeJSONSchema(value)
				property := commandCodeRecord(value)
				isOptional, _ := property["optional"].(bool)
				if _, explicit := record["required"]; !explicit && !isOptional && !optional[key] {
					required = append(required, key)
				}
			}
		}
		out := map[string]any{"type": "object"}
		if len(properties) > 0 {
			out["properties"] = properties
		}
		if len(required) > 0 {
			out["required"] = required
		}
		return out
	case "array":
		items := record["items"]
		if items == nil {
			items = record["element"]
		}
		return map[string]any{"type": "array", "items": commandCodeJSONSchema(items)}
	case "union":
		variants := commandCodeAnySlice(record["variants"])
		if len(variants) == 0 {
			variants = commandCodeAnySlice(record["anyOf"])
		}
		converted := make([]any, 0, len(variants))
		for _, variant := range variants {
			convertedVariant := commandCodeRecord(commandCodeJSONSchema(variant))
			if len(convertedVariant) > 0 {
				converted = append(converted, convertedVariant)
			}
		}
		if len(converted) > 0 {
			return map[string]any{"anyOf": converted}
		}
		return map[string]any{}
	case "optional":
		wrapped := record["wrapped"]
		if wrapped == nil {
			wrapped = record["inner"]
		}
		return commandCodeJSONSchema(wrapped)
	default:
		return map[string]any{}
	}
}

func commandCodeProjectSlug(pathName string) string {
	value := strings.ToLower(pathName)
	if len(value) >= 2 && value[1] == ':' && value[0] >= 'a' && value[0] <= 'z' {
		value = value[2:]
	}
	var slug strings.Builder
	pendingDash := false
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			if pendingDash && slug.Len() > 0 {
				slug.WriteByte('-')
			}
			slug.WriteRune(char)
			pendingDash = false
		} else {
			pendingDash = true
		}
	}
	if slug.Len() == 0 {
		return "project"
	}
	return slug.String()
}

func newCommandCodeThreadID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func resolveCommandCodeGenerateURL(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		trimmed = commandCodeDefaultBaseURL
	}
	if strings.HasSuffix(trimmed, "/alpha/generate") {
		return trimmed
	}
	return trimmed + "/alpha/generate"
}

func decodeCommandCodeResponseBody(response *http.Response) error {
	switch strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Encoding"))) {
	case "", "identity":
		return nil
	case "gzip":
		reader, err := gzip.NewReader(response.Body)
		if err != nil {
			return fmt.Errorf("decode Command Code gzip response: %w", err)
		}
		response.Body = &commandCodeDecodedBody{Reader: reader, decoder: reader, upstream: response.Body}
		return nil
	case "deflate":
		reader, err := zlib.NewReader(response.Body)
		if err != nil {
			return fmt.Errorf("decode Command Code deflate response: %w", err)
		}
		response.Body = &commandCodeDecodedBody{Reader: reader, decoder: reader, upstream: response.Body}
		return nil
	default:
		return fmt.Errorf("unsupported Command Code content encoding %q", response.Header.Get("Content-Encoding"))
	}
}

type commandCodeDecodedBody struct {
	io.Reader
	decoder  io.Closer
	upstream io.Closer
}

func (body *commandCodeDecodedBody) Close() error {
	decoderErr := body.decoder.Close()
	upstreamErr := body.upstream.Close()
	if decoderErr != nil {
		return decoderErr
	}
	return upstreamErr
}

func isCommandCodeRetryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500 && status < 600
}

func commandCodeRetryDelay(attempt int, retryAfter string, maxDelayMs int) (time.Duration, error) {
	if strings.TrimSpace(retryAfter) != "" {
		delay, err := commandCodeRetryAfterDelay(retryAfter)
		if err == nil {
			if delay > time.Duration(maxDelayMs)*time.Millisecond {
				return 0, fmt.Errorf("Retry-After delay %s exceeds max %dms", delay, maxDelayMs)
			}
			return delay, nil
		}
	}
	delayMs := commandCodeBaseRetryDelay
	for index := 0; index < attempt && delayMs < maxDelayMs; index++ {
		delayMs = min(delayMs*2, maxDelayMs)
	}
	jitterMs := float64(delayMs) * 0.2 * mathrand.Float64()
	delay := time.Duration(float64(delayMs)+jitterMs) * time.Millisecond
	return min(delay, time.Duration(maxDelayMs)*time.Millisecond), nil
}

func commandCodeRetryAfterDelay(value string) (time.Duration, error) {
	if seconds, err := time.ParseDuration(strings.TrimSpace(value) + "s"); err == nil && seconds >= 0 {
		return seconds, nil
	}
	date, err := http.ParseTime(strings.TrimSpace(value))
	if err != nil {
		return 0, err
	}
	return max(time.Duration(0), time.Until(date)), nil
}

func waitCommandCodeRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
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

func mapCommandCodeFinishReason(reason string) StopReason {
	switch reason {
	case "tool-calls":
		return StopReasonToolUse
	case "length", "max_tokens", "max-tokens", "max_output_tokens":
		return StopReasonLength
	default:
		return StopReasonStop
	}
}

func commandCodeRecord(value any) map[string]any {
	if record, ok := value.(map[string]any); ok {
		return cloneMap(record)
	}
	if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
		var record map[string]any
		if json.Unmarshal([]byte(text), &record) == nil {
			return record
		}
	}
	return map[string]any{}
}

func commandCodeInt(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	default:
		return 0
	}
}

func commandCodeStringSlice(value any) []string {
	items := commandCodeAnySlice(value)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func commandCodeAnySlice(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case []string:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = item
		}
		return result
	default:
		return nil
	}
}

func commandCodeStringSet(value any) map[string]bool {
	result := map[string]bool{}
	for _, item := range commandCodeStringSlice(value) {
		result[item] = true
	}
	return result
}
