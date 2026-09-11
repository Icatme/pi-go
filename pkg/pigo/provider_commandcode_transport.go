package pigo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Only Go-plan upgrade denials are cached. Bound the process-local cache and
// hash credential+endpoint identities so it neither retains raw keys nor lets
// requests for one account change another account's transport.
var commandCodeGenerateRoutes = struct {
	sync.Mutex
	keys  map[[32]byte]bool
	order [][32]byte
}{keys: make(map[[32]byte]bool)}

func commandCodeUsesGenerate(key [32]byte) bool {
	commandCodeGenerateRoutes.Lock()
	defer commandCodeGenerateRoutes.Unlock()
	return commandCodeGenerateRoutes.keys[key]
}

func commandCodeRememberGenerate(key [32]byte) {
	commandCodeGenerateRoutes.Lock()
	defer commandCodeGenerateRoutes.Unlock()
	if commandCodeGenerateRoutes.keys[key] {
		return
	}
	if len(commandCodeGenerateRoutes.order) == 64 {
		delete(commandCodeGenerateRoutes.keys, commandCodeGenerateRoutes.order[0])
		commandCodeGenerateRoutes.order = commandCodeGenerateRoutes.order[1:]
	}
	commandCodeGenerateRoutes.keys[key] = true
	commandCodeGenerateRoutes.order = append(commandCodeGenerateRoutes.order, key)
}

// This observer sits outside the already bounded/evidence-reporting provider
// HTTP transport. Restore the body verbatim for the native protocol adapter.
type commandCodeTransportProbe struct {
	base    http.RoundTripper
	upgrade atomic.Bool
	invalid atomic.Bool
}

func (p *commandCodeTransportProbe) RoundTrip(request *http.Request) (*http.Response, error) {
	p.invalid.Store(false)
	response, err := p.base.RoundTrip(request)
	if err != nil || response == nil || response.StatusCode != http.StatusForbidden || response.Body == nil || p.invalid.Load() {
		return response, err
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, DefaultMaxProviderErrorBytes+1))
	response.Body = &commandCodeReplayBody{Reader: io.MultiReader(bytes.NewReader(body), response.Body), Closer: response.Body}
	if readErr != nil || int64(len(body)) > DefaultMaxProviderErrorBytes {
		return response, err
	}
	var envelope map[string]any
	if json.Unmarshal(body, &envelope) == nil {
		target := envelope
		if nested, ok := envelope["error"].(map[string]any); ok {
			target = nested
		}
		if target["code"] == "upgrade_required" {
			p.upgrade.Store(true)
		}
	}
	return response, err
}

type commandCodeReplayBody struct {
	io.Reader
	io.Closer
}

// Native adapters own individual attempts. Only the routed output owns the
// caller's Observer, so a capability probe cannot finish the logical request.
type commandCodeLogicalObservation struct {
	output *AssistantMessageEventStream
	once   sync.Once
	ctx    context.Context
}

func (o *commandCodeLogicalObservation) start(payload any) context.Context {
	o.once.Do(func() { o.ctx = o.output.startRequest(o.ctx, payload) })
	return o.ctx
}

type commandCodeAttemptObserver struct {
	logical   *commandCodeLogicalObservation
	timeoutMs int
	cancel    context.CancelFunc
}

func (o *commandCodeAttemptObserver) OnRequestStart(_ context.Context, _ Model, payload any) context.Context {
	// Start from the logical context returned by the caller, not the previous
	// adapter's timeout context, which is canceled when that attempt completes.
	ctx, cancel := providerRequestContext(o.logical.start(payload), o.timeoutMs)
	o.cancel = cancel
	return ctx
}
func (*commandCodeAttemptObserver) OnRequestComplete(context.Context, Model, AssistantMessage, time.Duration) {
}
func (*commandCodeAttemptObserver) OnRequestError(context.Context, Model, error, time.Duration) {}
func (*commandCodeAttemptObserver) OnStreamEvent(context.Context, Model, AssistantMessageEvent) {}
func (o *commandCodeAttemptObserver) OnStreamFinish(context.Context, Model, AssistantMessage, int, int, time.Duration) {
	if o.cancel != nil {
		o.cancel()
	}
}
func streamCommandCode(model Model, ctx Context, options ProviderStreamOptions) *AssistantMessageEventStream {
	options = normalizeCommandCodeProviderStreamOptions(model, options)
	apiKey := usableCommandCodeAPIKey(options.APIKey)
	if apiKey == "" {
		apiKey = ResolveCommandCodeAPIKey(options.Auth)
	}
	if apiKey == "" {
		return streamAPIUnavailable(model, "missing Command Code API key; run pigo login commandcode or set COMMAND_CODE_API_KEY")
	}
	options.APIKey = apiKey
	options.Headers = mergeRequestHeaders(model.Headers, options.Headers)
	if options.SessionID != "" {
		options.Headers = mergeRequestHeaders(map[string]string{"x-session-id": options.SessionID}, options.Headers)
	}
	if commandCodeZDR() {
		options.Headers = mergeRequestHeaders(map[string]string{"x-cmd-zdr": "1"}, options.Headers)
	}
	imageContext, err := commandCodeImageContext(model, ctx)
	if err != nil {
		return streamAPIUnavailable(model, err.Error())
	}
	ctx = imageContext
	headers := mergeRequestHeaders(map[string]string{"Authorization": "Bearer " + apiKey}, options.Headers)
	identity := ""
	for name, value := range headers {
		if strings.EqualFold(name, "Authorization") {
			identity = value
		}
	}
	key := sha256.Sum256([]byte(commandCodeProviderBase(model.BaseURL) + "\x00" + identity))
	if commandCodeUsesGenerate(key) {
		return streamCommandCodeGenerate(model, ctx, options)
	}

	output := newAssistantMessageEventStream()
	output.setObserver(options.Observer, model)
	logical := &commandCodeLogicalObservation{output: output, ctx: options.RequestContext}
	go func() {
		nativeModel := commandCodeNativeModel(model)
		nativeOptions := options
		nativeOptions.Observer = &commandCodeAttemptObserver{logical: logical, timeoutMs: options.TimeoutMs}
		nativeOptions.TimeoutMs = 0
		client := *providerHTTPClient(options.HTTPClient)
		probe := &commandCodeTransportProbe{}
		bounded := *client.Transport.(*providerHTTPTransport)
		observe := bounded.policy.Observe
		bounded.policy.Observe = func(observation HTTPObservation) {
			if observation.ErrorBodyTruncated || observation.Err != nil {
				probe.invalid.Store(true)
			}
			if observe != nil {
				observe(observation)
			}
		}
		probe.base = &bounded
		client.Transport = probe
		nativeOptions.HTTPClient = &client
		// The caller sees only the response from the chosen protocol. HTTP evidence
		// remains attached to every actual attempt, including the upgrade denial.
		nativeOptions.OnResponse = func(response ProviderResponse, _ Model) {
			if !probe.upgrade.Load() && options.OnResponse != nil {
				options.OnResponse(response, model)
			}
		}
		nativeOptions.OnPayload = func(payload any, _ Model) any {
			if options.OnPayload != nil {
				if next := options.OnPayload(payload, model); next != nil {
					payload = next
				}
			}
			logical.start(payload)
			return payload
		}
		nativeOptions.Headers = mergeRequestHeaders(map[string]string{"Authorization": "Bearer " + apiKey}, nativeOptions.Headers)
		var source *AssistantMessageEventStream
		if nativeModel.API == "anthropic-messages" {
			source = streamAnthropicMessages(nativeModel, ctx, nativeOptions)
		} else {
			source = streamOpenAICompletions(nativeModel, ctx, nativeOptions)
		}
		pending := []AssistantMessageEvent{}
		for event := range source.Events() {
			if event.Type == AssistantMessageEventStart {
				pending = append(pending, event)
				continue
			}
			if probe.upgrade.Load() {
				continue
			}
			for _, start := range pending {
				output.push(start)
			}
			pending = nil
			output.push(event)
		}
		result := source.Result()
		if probe.upgrade.Load() {
			commandCodeRememberGenerate(key)
			fallbackOptions := options
			fallbackOptions.Observer = &commandCodeAttemptObserver{logical: logical}
			fallback := streamCommandCodeGenerate(model, ctx, fallbackOptions)
			for event := range fallback.Events() {
				output.push(event)
			}
			result = fallback.Result()
		}
		output.finish(result)
	}()
	return output
}

func commandCodeProviderBase(base string) string {
	if strings.TrimSpace(base) == "" {
		base = resolveCommandCodeAPIBaseURL()
	}
	base = strings.TrimRight(base, "/")
	base = strings.TrimSuffix(base, "/alpha/generate")
	if !strings.HasSuffix(base, "/provider/v1") {
		base += "/provider/v1"
	}
	return base
}

func commandCodeNativeModel(model Model) Model {
	native := cloneModel(model)
	native.BaseURL = commandCodeProviderBase(model.BaseURL)
	disabled := false
	if strings.HasPrefix(model.ID, "claude-") {
		native.API = "anthropic-messages"
		native.BaseURL = strings.TrimSuffix(native.BaseURL, "/v1")
		forceAdaptive := model.Reasoning
		native.Compat = &AnthropicMessagesCompat{SupportsEagerToolInputStreaming: &disabled, SupportsLongCacheRetention: &disabled, ForceAdaptiveThinking: &forceAdaptive}
	} else {
		native.API = "openai-completions"
		effort := len(commandCodeModelCatalog[model.ID].Efforts) > 0
		native.Compat = &OpenAICompletionsCompat{SupportsStore: &disabled, SupportsDeveloperRole: &disabled, SupportsReasoningEffort: &effort, MaxTokensField: "max_tokens"}
	}
	return native
}
