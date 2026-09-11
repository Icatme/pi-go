package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIResponsesCachePayloadUsesExplicitModelCapabilities(t *testing.T) {
	yes, no := true, false
	for _, test := range []struct {
		name          string
		compat        *OpenAIResponsesCompat
		retention     CacheRetention
		wantRetention string
		wantOptions   *openAIResponsesPromptCacheOptions
	}{
		{name: "legacy long", retention: CacheRetentionLong, wantRetention: "24h"},
		{name: "unknown gateway with GPT-5.6 ID", compat: &OpenAIResponsesCompat{}, retention: CacheRetentionLong, wantRetention: "24h"},
		{name: "explicit long", compat: &OpenAIResponsesCompat{SupportsExplicitPromptCacheMode: &yes}, retention: CacheRetentionLong, wantOptions: &openAIResponsesPromptCacheOptions{TTL: "30m"}},
		{name: "explicit none", compat: &OpenAIResponsesCompat{SupportsExplicitPromptCacheMode: &yes}, retention: CacheRetentionNone, wantOptions: &openAIResponsesPromptCacheOptions{Mode: "explicit"}},
		{name: "explicit short", compat: &OpenAIResponsesCompat{SupportsExplicitPromptCacheMode: &yes}, retention: CacheRetentionShort},
		{name: "explicit default", compat: &OpenAIResponsesCompat{SupportsExplicitPromptCacheMode: &yes}},
		{name: "legacy none", retention: CacheRetentionNone},
		{name: "legacy long disabled", compat: &OpenAIResponsesCompat{SupportsLongCacheRetention: &no}, retention: CacheRetentionLong},
		{name: "explicit long disabled", compat: &OpenAIResponsesCompat{SupportsExplicitPromptCacheMode: &yes, SupportsLongCacheRetention: &no}, retention: CacheRetentionLong},
		{name: "explicit none with long disabled", compat: &OpenAIResponsesCompat{SupportsExplicitPromptCacheMode: &yes, SupportsLongCacheRetention: &no}, retention: CacheRetentionNone, wantOptions: &openAIResponsesPromptCacheOptions{Mode: "explicit"}},
		{name: "explicit mode disabled", compat: &OpenAIResponsesCompat{SupportsExplicitPromptCacheMode: &no}, retention: CacheRetentionLong, wantRetention: "24h"},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := Model{API: "openai-responses", Provider: "gateway", ID: "gpt-5.6-sol", Compat: test.compat}
			request := buildOpenAIResponsesRequest(model, Context{}, ProviderStreamOptions{SessionID: "session-1", CacheRetention: test.retention})
			if request.PromptCacheRetention != test.wantRetention {
				t.Fatalf("prompt_cache_retention = %q, want %q", request.PromptCacheRetention, test.wantRetention)
			}
			if (request.PromptCacheOptions == nil) != (test.wantOptions == nil) || (test.wantOptions != nil && *request.PromptCacheOptions != *test.wantOptions) {
				t.Fatalf("prompt_cache_options = %+v, want %+v", request.PromptCacheOptions, test.wantOptions)
			}
			wantKey := "session-1"
			if test.retention == CacheRetentionNone {
				wantKey = ""
			}
			if request.PromptCacheKey != wantKey {
				t.Fatalf("prompt_cache_key = %q, want %q", request.PromptCacheKey, wantKey)
			}
			payload, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(payload, &fields); err != nil {
				t.Fatal(err)
			}
			for field, wantPresent := range map[string]bool{"prompt_cache_key": wantKey != "", "prompt_cache_retention": test.wantRetention != "", "prompt_cache_options": test.wantOptions != nil} {
				if _, present := fields[field]; present != wantPresent {
					t.Errorf("%s presence = %v, want %v: %s", field, present, wantPresent, payload)
				}
			}
		})
	}
}

func TestOpenAIResponsesMaxOutputTokensCapability(t *testing.T) {
	yes, no := true, false
	for _, allowed := range []*bool{nil, &yes, &no} {
		model := Model{API: "openai-responses", Compat: &OpenAIResponsesCompat{SupportsMaxOutputTokens: allowed}}
		request := buildOpenAIResponsesRequest(model, Context{}, ProviderStreamOptions{MaxTokens: 1024})
		want := 1024
		if allowed != nil && !*allowed {
			want = 0
		}
		if request.MaxOutputTokens != want {
			t.Fatalf("max_output_tokens = %d, want %d", request.MaxOutputTokens, want)
		}
		payload, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(payload, &fields); err != nil {
			t.Fatal(err)
		}
		if _, present := fields["max_output_tokens"]; present != (want != 0) {
			t.Fatalf("max_output_tokens presence does not match capability: %s", payload)
		}
	}
}

func TestOpenAIResponsesSessionAffinityRespectsCacheAndCompat(t *testing.T) {
	no := false
	for _, test := range []struct {
		name        string
		retention   CacheRetention
		compat      *OpenAIResponsesCompat
		wantSession string
		wantRequest string
	}{
		{name: "cache enabled", retention: CacheRetentionShort, wantSession: "session-1", wantRequest: "session-1"},
		{name: "cache disabled", retention: CacheRetentionNone},
		{name: "session header disabled", retention: CacheRetentionShort, compat: &OpenAIResponsesCompat{SendSessionIdHeader: &no}, wantRequest: "session-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			type receivedRequest struct {
				header http.Header
				body   openAIResponsesRequest
			}
			received := make(chan receivedRequest, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body openAIResponsesRequest
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				received <- receivedRequest{header: r.Header.Clone(), body: body}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: "+`{"type":"response.completed","response":{"status":"completed","output":[],"usage":{"input_tokens":0,"output_tokens":0}}}`+"\n\n")
			}))
			defer server.Close()
			model := Model{API: "openai-responses", Provider: "gateway", ID: "test", BaseURL: server.URL, Compat: test.compat}
			result := Stream(model, Context{}, ProviderStreamOptions{APIKey: "test", SessionID: "session-1", CacheRetention: test.retention}).Result()
			if result.StopReason != StopReasonStop {
				t.Fatalf("local response failed: %+v", result)
			}
			request := <-received
			if request.header.Get("session_id") != test.wantSession || request.header.Get("x-client-request-id") != test.wantRequest {
				t.Fatalf("unexpected generated session affinity headers: %v", request.header)
			}
		})
	}
}
