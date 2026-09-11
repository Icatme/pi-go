package pigo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCommandCodeProviderRoutingAndEvidence(t *testing.T) {
	var mu sync.Mutex
	paths := []string{}
	handler := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Header.Get("Authorization")+" "+r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/provider/v1/chat/completions" && r.Header.Get("Authorization") == "Bearer go-key" {
			w.WriteHeader(403)
			io.WriteString(w, `{"error":{"code":"upgrade_required"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch r.URL.Path {
		case "/provider/v1/chat/completions":
			fmt.Fprintln(w, `data: {"model":"actual-command-model","choices":[{"index":0,"delta":{"content":"provider"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":3}}`)
			fmt.Fprint(w, "\ndata: [DONE]\n")
		case "/alpha/generate":
			fmt.Fprintln(w, `{"type":"text-delta","text":"generate"}`)
			fmt.Fprintln(w, `{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":12,"outputTokens":3}}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()
	statuses := []int{}
	observations := []HTTPObservation{}
	client := NewProviderHTTPClient(server.Client(), ProviderHTTPPolicy{Observe: func(observation HTTPObservation) { observations = append(observations, observation) }})
	model := commandCodeTestModel(server.URL)
	for _, key := range []string{"go-key", "go-key", "provider-key"} {
		stream := StreamSimple(model, Context{}, SimpleStreamOptions{APIKey: key, HTTPClient: client, OnResponse: func(response ProviderResponse, _ Model) { statuses = append(statuses, response.Status) }})
		events := collectCommandCodeEvents(stream)
		result := stream.Result()
		wantAPI := API("commandcode-custom")
		if key == "provider-key" {
			wantAPI = "openai-completions"
		}
		if result.StopReason != StopReasonStop || !result.UsageReported || result.Usage.Input != 12 || result.API != wantAPI {
			t.Fatalf("bad result: %+v", result)
		}
		starts, ends := 0, 0
		for _, event := range events {
			if event.Type == AssistantMessageEventStart {
				starts++
			}
			if event.Type == AssistantMessageEventDone {
				ends++
			}
			if event.Type == AssistantMessageEventError {
				t.Fatalf("probe error leaked: %+v", event)
			}
		}
		if starts != 1 || ends != 1 {
			t.Fatalf("start=%d done=%d events=%+v", starts, ends, events)
		}
		if key == "provider-key" && result.ResponseModel != "actual-command-model" {
			t.Fatalf("lost response model: %+v", result)
		}
	}
	if len(paths) != 4 || paths[0] != "Bearer go-key /provider/v1/chat/completions" || paths[1] != "Bearer go-key /alpha/generate" || paths[2] != "Bearer go-key /alpha/generate" || paths[3] != "Bearer provider-key /provider/v1/chat/completions" {
		t.Fatalf("wrong routes: %v", paths)
	}
	if fmt.Sprint(statuses) != "[200 200 200]" {
		t.Fatalf("public responses include probe: %v", statuses)
	}
	if len(observations) != 4 || observations[0].StatusCode != 403 || observations[0].ErrorCode != "upgrade_required" {
		t.Fatalf("lost HTTP evidence: %+v", observations)
	}
	other := httptest.NewServer(http.HandlerFunc(handler))
	defer other.Close()
	model.BaseURL = other.URL
	result := CompleteSimple(model, Context{}, SimpleStreamOptions{APIKey: "go-key"})
	if result.StopReason != StopReasonStop || len(paths) != 6 {
		t.Fatalf("endpoint shared route decision: %v %+v", paths, result)
	}
}

func TestCommandCodeProviderDoesNotFallbackForOtherFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"auth", 401, `{"error":{"code":"upgrade_required"}}`},
		{"permission", 403, `{"error":{"code":"forbidden"}}`},
		{"nested takes precedence", 403, `{"code":"upgrade_required","error":{"code":"forbidden"}}`},
		{"malformed", 403, `{"error":{"code":"upgrade_required"}`},
		{"rate limit", 429, `{"error":{"code":"upgrade_required"}}`},
		{"server", 503, `{"error":{"code":"upgrade_required"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/provider/v1/chat/completions" {
					t.Errorf("unexpected fallback %s", r.URL.Path)
				}
				w.WriteHeader(test.status)
				io.WriteString(w, test.body)
			}))
			defer server.Close()
			result := CompleteSimple(commandCodeTestModel(server.URL), Context{}, SimpleStreamOptions{APIKey: "key"})
			if requests != 1 || result.StopReason != StopReasonError {
				t.Fatalf("requests=%d result=%+v", requests, result)
			}
		})
	}
	requests := 0
	client := &http.Client{Transport: commandCodeRoundTripFunc(func(r *http.Request) (*http.Response, error) { requests++; return nil, fmt.Errorf("offline") })}
	result := CompleteSimple(commandCodeTestModel("https://commandcode.invalid"), Context{}, SimpleStreamOptions{APIKey: "key", HTTPClient: client})
	if requests != 1 || result.StopReason != StopReasonError {
		t.Fatalf("network failure retried via generate: %d %+v", requests, result)
	}
}

type commandCodeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f commandCodeRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCommandCodeProviderUsesAnthropicProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/provider/v1/messages" {
			t.Errorf("wrong path %s", r.URL.Path)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if commandCodeRecord(body["thinking"])["type"] != "adaptive" || commandCodeRecord(body["output_config"])["effort"] != "xhigh" {
			t.Errorf("missing adaptive thinking: %+v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_cc\",\"model\":\"claude-fable-5\",\"usage\":{\"input_tokens\":4,\"output_tokens\":0}}}\n\n")
		fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n")
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	model := newCommandCodeModel("claude-fable-5", "Claude", 200000, 65536, UsageCost{})
	model.BaseURL = server.URL
	stream := StreamSimple(model, Context{}, SimpleStreamOptions{APIKey: "key", Reasoning: ThinkingLevelXHigh})
	events := collectCommandCodeEvents(stream)
	result := stream.Result()
	if result.StopReason != StopReasonStop || !result.UsageReported || result.API != "anthropic-messages" {
		t.Fatalf("bad result: %+v events=%+v", result, events)
	}
}

func TestCommandCodeGenerateTerminalFailures(t *testing.T) {
	for _, reason := range []string{"upstream_error", "network-error", "connection error"} {
		t.Run(reason, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintln(w, `{"type":"text-delta","text":"partial"}`)
				json.NewEncoder(w).Encode(map[string]any{"type": "finish", "finishReason": "stop", "rawFinishReason": reason})
			}))
			defer server.Close()
			result := completeCommandCodeGenerateForTest(commandCodeTestModel(server.URL), Context{}, SimpleStreamOptions{APIKey: "key"})
			if result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, "connection failed") {
				t.Fatalf("false success: %+v", result)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, `{"type":"abort"}`) }))
	defer server.Close()
	result := completeCommandCodeGenerateForTest(commandCodeTestModel(server.URL), Context{}, SimpleStreamOptions{APIKey: "key"})
	if result.StopReason != StopReasonAborted {
		t.Fatalf("abort lost: %+v", result)
	}
}

func TestCommandCodeGenerateRequestMetadata(t *testing.T) {
	model := newCommandCodeModel("meta/muse-spark-1.3", "Muse", 1048576, 65536, UsageCost{})
	temperature := 0.7
	session := "71b96a12-cd4b-45fd-9f22-0768f3996000"
	request, _, err := buildCommandCodeRequest(model, Context{}, ProviderStreamOptions{Reasoning: ThinkingLevelXHigh, Temperature: &temperature, SessionID: session})
	if err != nil || request.ThreadID != session || request.Params.ReasoningEffort != "xhigh" || request.Params.Temperature == nil || *request.Params.Temperature != 0.7 {
		t.Fatalf("request=%+v err=%v", request, err)
	}
	request, _, err = buildCommandCodeRequest(model, Context{}, ProviderStreamOptions{Reasoning: ThinkingLevelMax, SessionID: "not-a-uuid"})
	if err != nil || request.ThreadID != "" || request.Params.ReasoningEffort != "" || request.Params.Temperature != nil {
		t.Fatalf("unsupported effort/session not omitted: %+v err=%v", request, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := CompleteSimple(model, Context{}, SimpleStreamOptions{APIKey: "key", RequestContext: ctx})
	if result.StopReason != StopReasonAborted {
		t.Fatalf("cancellation lost: %+v", result)
	}
}

func TestCommandCodeGenerateStreamsToolArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"type":"tool-input-start","id":"call","toolName":"read"}`)
		fmt.Fprintln(w, `{"type":"tool-input-delta","id":"call","delta":"{\"path\":"}`)
		fmt.Fprintln(w, `{"type":"tool-input-delta","id":"call","delta":"\"a.go\"}"}`)
		fmt.Fprintln(w, `{"type":"tool-input-end","id":"call"}`)
		fmt.Fprintln(w, `{"type":"tool-call","toolCallId":"call","toolName":"read","input":{"path":"final.go"}}`)
		fmt.Fprintln(w, `{"type":"finish","finishReason":"tool-calls"}`)
	}))
	defer server.Close()
	stream := commandCodeGenerateSimpleForTest(commandCodeTestModel(server.URL), Context{}, SimpleStreamOptions{APIKey: "key"})
	events := collectCommandCodeEvents(stream)
	result := stream.Result()
	starts, deltas, ends := 0, 0, 0
	for _, event := range events {
		switch event.Type {
		case AssistantMessageEventToolCallStart:
			starts++
		case AssistantMessageEventToolCallDelta:
			deltas++
		case AssistantMessageEventToolCallEnd:
			ends++
		}
	}
	if starts != 1 || deltas != 2 || ends != 1 || result.StopReason != StopReasonToolUse || len(result.Content) != 1 {
		t.Fatalf("events=%+v result=%+v", events, result)
	}
	if call := result.Content[0].(ToolCall); call.Arguments["path"] != "final.go" {
		t.Fatalf("final arguments not authoritative: %+v", call)
	}
}

func TestCommandCodeConcurrentCredentialDetection(t *testing.T) {
	oldStarted, releaseOld := make(chan struct{}), make(chan struct{})
	requests := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Header.Get("Authorization") + " " + r.URL.Path
		if r.URL.Path == "/provider/v1/chat/completions" && r.Header.Get("Authorization") == "Bearer old-go" {
			close(oldStarted)
			<-releaseOld
			w.WriteHeader(403)
			io.WriteString(w, `{"error":{"code":"upgrade_required"}}`)
			return
		}
		if r.URL.Path == "/alpha/generate" {
			fmt.Fprintln(w, `{"type":"finish","finishReason":"stop"}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	model := commandCodeTestModel(server.URL)
	old := StreamSimple(model, Context{}, SimpleStreamOptions{APIKey: "old-go"})
	<-oldStarted
	result := CompleteSimple(model, Context{}, SimpleStreamOptions{APIKey: "new-provider"})
	if result.StopReason != StopReasonStop {
		close(releaseOld)
		t.Fatalf("new credential failed: %+v", result)
	}
	close(releaseOld)
	collectCommandCodeEvents(old)
	if result = old.Result(); result.StopReason != StopReasonStop {
		t.Fatal(result.ErrorMessage)
	}
	result = CompleteSimple(model, Context{}, SimpleStreamOptions{APIKey: "new-provider"})
	if result.StopReason != StopReasonStop {
		t.Fatal(result.ErrorMessage)
	}
	got := []string{<-requests, <-requests, <-requests, <-requests}
	if got[3] != "Bearer new-provider /provider/v1/chat/completions" {
		t.Fatalf("stale credential changed new route: %v", got)
	}
}

func TestCommandCodeTruncatedErrorCannotSelectFallback(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/provider/v1/chat/completions" {
			t.Errorf("truncated denial triggered fallback: %s", r.URL.Path)
		}
		w.WriteHeader(403)
		io.WriteString(w, `{"error":{"code":"upgrade_required"}}`+strings.Repeat(" ", 200))
	}))
	defer server.Close()
	var observations []HTTPObservation
	client := NewProviderHTTPClient(server.Client(), ProviderHTTPPolicy{MaxErrorBodyBytes: 80, Observe: func(o HTTPObservation) { observations = append(observations, o) }})
	result := CompleteSimple(commandCodeTestModel(server.URL), Context{}, SimpleStreamOptions{APIKey: "key", HTTPClient: client})
	if requests != 1 || result.StopReason != StopReasonError || len(observations) != 1 || !observations[0].ErrorBodyTruncated {
		t.Fatalf("requests=%d result=%+v evidence=%+v", requests, result, observations)
	}
}

type commandCodeObserverContextKey string

type commandCodeRecordingObserver struct {
	wireAPI   API
	t         *testing.T
	model     Model
	starts    int
	completes int
	errors    int
	finishes  int
	events    []AssistantMessageEventType
	done      chan struct{}
	cancel    bool
}

func (o *commandCodeRecordingObserver) check(ctx context.Context, model Model, returnedContext bool) {
	o.t.Helper()
	if model.ID != o.model.ID || model.Provider != o.model.Provider || model.API != o.model.API {
		o.t.Errorf("native model leaked to Observer: %+v", model)
	}
	if ctx.Value(commandCodeObserverContextKey("caller")) != "caller-value" {
		o.t.Errorf("caller context lost")
	}
	if returnedContext && ctx.Value(commandCodeObserverContextKey("observer")) != "observer-value" {
		o.t.Errorf("Observer context lost")
	}
}
func (o *commandCodeRecordingObserver) OnRequestStart(ctx context.Context, model Model, payload any) context.Context {
	o.check(ctx, model, false)
	o.starts++
	if payload == nil {
		o.t.Errorf("request Observer lost payload")
	}
	ctx = context.WithValue(ctx, commandCodeObserverContextKey("observer"), "observer-value")
	if o.cancel {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		return canceled
	}
	return ctx
}
func (o *commandCodeRecordingObserver) OnRequestComplete(ctx context.Context, model Model, message AssistantMessage, _ time.Duration) {
	o.check(ctx, model, true)
	o.completes++
	if message.API != o.wireAPI {
		o.t.Errorf("result protocol provenance changed: %+v", message)
	}
}
func (o *commandCodeRecordingObserver) OnRequestError(ctx context.Context, model Model, _ error, _ time.Duration) {
	o.check(ctx, model, true)
	o.errors++
}
func (o *commandCodeRecordingObserver) OnStreamEvent(ctx context.Context, model Model, event AssistantMessageEvent) {
	o.check(ctx, model, true)
	o.events = append(o.events, event.Type)
	for _, message := range []AssistantMessage{event.Partial, event.Message, event.Error} {
		if message.Model != "" && message.API != o.wireAPI {
			o.t.Errorf("event protocol provenance changed: %+v", event)
		}
	}
}
func (o *commandCodeRecordingObserver) OnStreamFinish(ctx context.Context, model Model, message AssistantMessage, count, dropped int, _ time.Duration) {
	o.check(ctx, model, true)
	o.finishes++
	if message.API != o.wireAPI || count != len(o.events) || dropped != 0 {
		o.t.Errorf("wrong final Observer event: %+v count=%d events=%v dropped=%d", message, count, o.events, dropped)
	}
	if o.finishes == 1 {
		close(o.done)
	}
}

func TestCommandCodeObserverOwnsLogicalRequest(t *testing.T) {
	for _, test := range []struct {
		name     string
		fallback bool
		status   int
	}{
		{name: "provider success", status: 200},
		{name: "generate fallback", fallback: true, status: 200},
		{name: "provider failure", status: 401},
		{name: "fallback failure", fallback: true, status: 500},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := []string{}
			var probeContext context.Context
			observations := []HTTPObservation{}
			model := commandCodeTestModel("https://" + strings.ReplaceAll(test.name, " ", "-") + ".invalid")
			transport := commandCodeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				paths = append(paths, request.URL.Path)
				if test.fallback {
					if request.URL.Path == "/provider/v1/chat/completions" {
						probeContext = request.Context()
					} else {
						select {
						case <-probeContext.Done():
						case <-time.After(time.Second):
							t.Error("native attempt context was not released")
						}
					}
				}
				if request.Context().Value(commandCodeObserverContextKey("caller")) != "caller-value" || request.Context().Value(commandCodeObserverContextKey("observer")) != "observer-value" {
					t.Errorf("Observer context did not reach %s", request.URL.Path)
				}
				if request.Context().Err() != nil {
					t.Errorf("completed probe canceled fallback context: %v", request.Context().Err())
				}
				if _, ok := request.Context().Deadline(); !ok {
					t.Errorf("request timeout lost for %s", request.URL.Path)
				}
				status := test.status
				body := `data: {"model":"command-result","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":1}}` + "\n\ndata: [DONE]\n\n"
				if test.fallback && request.URL.Path == "/provider/v1/chat/completions" {
					status = 403
					body = `{"error":{"code":"upgrade_required"}}`
				} else if status != 200 {
					body = `{"error":{"code":"final_failure","message":"final provider failure"}}`
				} else if request.URL.Path == "/alpha/generate" {
					body = "{\"type\":\"text-delta\",\"text\":\"ok\"}\n{\"type\":\"finish\",\"finishReason\":\"stop\"}\n"
				}
				return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d status", status), Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})
			client := NewProviderHTTPClient(&http.Client{Transport: transport}, ProviderHTTPPolicy{Observe: func(observation HTTPObservation) { observations = append(observations, observation) }})
			runs := 1
			if test.fallback && test.status == 200 {
				runs = 2
			}
			for i := 0; i < runs; i++ {
				wireAPI := API("openai-completions")
				if test.fallback {
					wireAPI = "commandcode-custom"
				}
				observer := &commandCodeRecordingObserver{t: t, model: model, wireAPI: wireAPI, done: make(chan struct{})}
				ctx := context.WithValue(context.Background(), commandCodeObserverContextKey("caller"), "caller-value")
				stream := StreamSimple(model, Context{}, SimpleStreamOptions{APIKey: "observer-key", HTTPClient: client, RequestContext: ctx, Observer: observer, TimeoutMs: 1000})
				events := collectCommandCodeEvents(stream)
				result := stream.Result()
				select {
				case <-observer.done:
				case <-time.After(time.Second):
					t.Fatal("missing Observer completion")
				}
				wantErrors := 0
				wantComplete := 1
				if test.status != 200 {
					wantErrors = 1
					wantComplete = 0
					if result.StopReason != StopReasonError {
						t.Errorf("lost final error: %+v", result)
					}
				}
				if observer.starts != 1 || observer.completes != wantComplete || observer.errors != wantErrors || observer.finishes != 1 {
					t.Fatalf("wrong logical lifecycle: starts=%d complete=%d errors=%d finish=%d", observer.starts, observer.completes, observer.errors, observer.finishes)
				}
				types := make([]AssistantMessageEventType, 0, len(events))
				for _, event := range events {
					types = append(types, event.Type)
				}
				if fmt.Sprint(observer.events) != fmt.Sprint(types) {
					t.Fatalf("Observer saw a different logical stream: observer=%v public=%v", observer.events, types)
				}
			}
			wantRequests := runs
			if test.fallback {
				wantRequests++
			}
			if len(paths) != wantRequests || len(observations) != wantRequests {
				t.Fatalf("physical HTTP evidence lost: paths=%v observations=%+v", paths, observations)
			}
			if test.fallback && observations[0].StatusCode != 403 {
				t.Fatal("upgrade probe evidence lost")
			}
		})
	}
}

func TestCommandCodeObserverContextCancellation(t *testing.T) {
	model := commandCodeTestModel("https://observer-cancel.invalid")
	observer := &commandCodeRecordingObserver{t: t, model: model, wireAPI: "openai-completions", done: make(chan struct{}), cancel: true}
	client := &http.Client{Transport: commandCodeRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Context().Err() != context.Canceled || request.Context().Value(commandCodeObserverContextKey("observer")) != "observer-value" {
			t.Errorf("Observer cancellation not propagated")
		}
		return nil, request.Context().Err()
	})}
	ctx := context.WithValue(context.Background(), commandCodeObserverContextKey("caller"), "caller-value")
	stream := StreamSimple(model, Context{}, SimpleStreamOptions{APIKey: "key", HTTPClient: client, RequestContext: ctx, Observer: observer})
	collectCommandCodeEvents(stream)
	result := stream.Result()
	select {
	case <-observer.done:
	case <-time.After(time.Second):
		t.Fatal("missing Observer finish")
	}
	if result.StopReason != StopReasonAborted || observer.starts != 1 || observer.errors != 1 || observer.completes != 0 || observer.finishes != 1 {
		t.Fatalf("canceled logical lifecycle: result=%+v observer=%+v", result, observer)
	}
}

func commandCodeClaudeToolRoundSSE() string {
	return buildAnthropicSSE(
		map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_first", "model": "claude-fable-5", "usage": map[string]any{"input_tokens": 5, "output_tokens": 0}}},
		map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "thinking", "thinking": ""}},
		map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": "plan"}},
		map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "signature_delta", "signature": "signed-plan"}},
		map[string]any{"type": "content_block_stop", "index": 0},
		map[string]any{"type": "content_block_start", "index": 1, "content_block": map[string]any{"type": "tool_use", "id": "call_first", "name": "read", "input": map[string]any{}}},
		map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"path":"a.go"}`}},
		map[string]any{"type": "content_block_stop", "index": 1},
		map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"}, "usage": map[string]any{"output_tokens": 3}},
		map[string]any{"type": "message_stop"},
	)
}

func commandCodeClaudeFinalSSE() string {
	return buildAnthropicSSE(
		map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_final", "model": "claude-fable-5", "usage": map[string]any{"input_tokens": 8, "output_tokens": 0}}},
		map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}, "usage": map[string]any{"output_tokens": 2}},
		map[string]any{"type": "message_stop"},
	)
}

func TestCommandCodeNativeTwoTurnReasoningAndTools(t *testing.T) {
	for _, test := range []struct {
		name    string
		modelID string
		api     API
		first   string
		final   string
	}{
		{name: "Claude", modelID: "claude-fable-5", api: "anthropic-messages", first: commandCodeClaudeToolRoundSSE(), final: commandCodeClaudeFinalSSE()},
		{name: "Completions", modelID: "gpt-5.6-luna", api: "openai-completions",
			first: `data: {"model":"gpt-5.6-luna","choices":[{"index":0,"delta":{"reasoning_content":"plan","tool_calls":[{"index":0,"id":"call_first","type":"function","function":{"name":"read","arguments":"{\"path\":\"a.go\"}"}}]},"finish_reason":null}]}` + "\n\n" + `data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}` + "\n\ndata: [DONE]\n\n",
			final: `data: {"choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":2}}` + "\n\ndata: [DONE]\n\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			bodies := []map[string]any{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				bodies = append(bodies, body)
				w.Header().Set("Content-Type", "text/event-stream")
				if len(bodies) == 1 {
					io.WriteString(w, test.first)
				} else {
					io.WriteString(w, test.final)
				}
			}))
			defer server.Close()
			model := newCommandCodeModel(test.modelID, test.name, 200000, 65536, UsageCost{})
			model.BaseURL = server.URL
			ctx := Context{Messages: []Message{UserMessage{Content: "read a.go"}}, Tools: []Tool{{Name: "read", Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}}}
			firstStream := StreamSimple(model, ctx, SimpleStreamOptions{APIKey: "key", Reasoning: ThinkingLevelHigh})
			events := collectCommandCodeEvents(firstStream)
			first := firstStream.Result()
			if first.StopReason != StopReasonToolUse || first.API != test.api || len(first.Content) != 2 {
				t.Fatalf("first native result lost provenance: %+v", first)
			}
			thought, ok := first.Content[0].(ThinkingContent)
			if !ok || thought.Thinking != "plan" || thought.ThinkingSignature == "" {
				t.Fatalf("first reasoning missing: %+v", first.Content)
			}
			for _, event := range events {
				for _, message := range []AssistantMessage{event.Partial, event.Message, event.Error} {
					if message.Model != "" && message.API != test.api {
						t.Fatalf("event protocol changed: %+v", event)
					}
				}
			}
			ctx.Messages = append(ctx.Messages, first, ToolResultMessage{ToolCallID: "call_first", ToolName: "read", Content: []ContentBlock{TextContent{Text: "file contents"}}})
			before, _ := json.Marshal(ctx)
			secondStream := StreamSimple(model, ctx, SimpleStreamOptions{APIKey: "key", Reasoning: ThinkingLevelHigh})
			collectCommandCodeEvents(secondStream)
			second := secondStream.Result()
			if second.StopReason != StopReasonStop || second.API != test.api || len(bodies) != 2 {
				t.Fatalf("second native round failed: %+v requests=%d", second, len(bodies))
			}
			after, _ := json.Marshal(ctx)
			if string(before) != string(after) || model.API != "commandcode-custom" {
				t.Fatal("native replay mutated caller context or model")
			}
			messages := commandCodeAnySlice(bodies[1]["messages"])
			if len(messages) != 3 {
				t.Fatalf("tool history not preserved: %+v", messages)
			}
			assistant := commandCodeRecord(messages[1])
			if test.api == "anthropic-messages" {
				parts := commandCodeAnySlice(assistant["content"])
				if len(parts) != 2 || commandCodeRecord(parts[0])["type"] != "thinking" || commandCodeRecord(parts[0])["signature"] != "signed-plan" || commandCodeRecord(parts[0])["thinking"] != "plan" || commandCodeRecord(parts[1])["type"] != "tool_use" {
					t.Fatalf("Claude signed tool history changed: %+v", assistant)
				}
				toolResult := commandCodeRecord(commandCodeAnySlice(commandCodeRecord(messages[2])["content"])[0])
				if toolResult["type"] != "tool_result" || toolResult["tool_use_id"] != "call_first" {
					t.Fatalf("tool result lost call identity: %+v", toolResult)
				}
			} else {
				if assistant["reasoning_content"] != "plan" || assistant["content"] != nil {
					t.Fatalf("Completions reasoning became visible text: %+v", assistant)
				}
				calls := commandCodeAnySlice(assistant["tool_calls"])
				if len(calls) != 1 || commandCodeRecord(calls[0])["id"] != "call_first" || commandCodeRecord(messages[2])["tool_call_id"] != "call_first" {
					t.Fatalf("Completions tool chain changed: %+v", messages)
				}
			}
		})
	}
}

func TestCommandCodeNativeRejectsForeignThinkingOrigins(t *testing.T) {
	for _, api := range []API{"anthropic-messages", "openai-completions"} {
		for _, origin := range []string{"generate", "other model", "other provider", "other native protocol"} {
			t.Run(string(api)+"/"+origin, func(t *testing.T) {
				id := "gpt-5.6-luna"
				signature := "reasoning_content"
				if api == "anthropic-messages" {
					id = "claude-fable-5"
					signature = "foreign-signature"
				}
				var body map[string]any
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if api == "anthropic-messages" {
						io.WriteString(w, commandCodeClaudeFinalSSE())
					} else {
						fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					}
				}))
				defer server.Close()
				model := newCommandCodeModel(id, id, 200000, 65536, UsageCost{})
				model.BaseURL = server.URL
				prior := AssistantMessage{API: api, Provider: "commandcode", Model: id, StopReason: StopReasonStop, Content: []ContentBlock{ThinkingContent{Thinking: "foreign thought", ThinkingSignature: signature}}}
				switch origin {
				case "generate":
					prior.API = "commandcode-custom"
				case "other model":
					prior.Model = "other-model"
				case "other provider":
					prior.Provider = "other-provider"
				case "other native protocol":
					if api == "anthropic-messages" {
						prior.API = "openai-completions"
					} else {
						prior.API = "anthropic-messages"
					}
				}
				ctx := Context{Messages: []Message{prior, UserMessage{Content: "continue"}}}
				before, _ := json.Marshal(ctx)
				result := CompleteSimple(model, ctx, SimpleStreamOptions{APIKey: "key"})
				if result.StopReason != StopReasonStop {
					t.Fatal(result.ErrorMessage)
				}
				after, _ := json.Marshal(ctx)
				if string(before) != string(after) {
					t.Fatal("foreign history was mutated")
				}
				messages := commandCodeAnySlice(body["messages"])
				if len(messages) < 1 {
					t.Fatalf("missing history: %+v", body)
				}
				assistant := commandCodeRecord(messages[0])
				if api == "anthropic-messages" {
					for _, part := range commandCodeAnySlice(assistant["content"]) {
						if commandCodeRecord(part)["type"] == "thinking" || commandCodeRecord(part)["signature"] != nil {
							t.Fatalf("accepted foreign signature: %+v", assistant)
						}
					}
				} else if assistant["reasoning_content"] != nil || assistant["reasoning_details"] != nil {
					t.Fatalf("accepted foreign reasoning provenance: %+v", assistant)
				}
			})
		}
	}
}
