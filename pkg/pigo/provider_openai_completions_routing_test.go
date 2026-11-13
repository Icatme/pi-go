package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAICompletionsRoutingWire(t *testing.T) {
	no := false
	cases := []struct {
		name   string
		compat *OpenAICompletionsCompat
		want   string
	}{
		{"openrouter", &OpenAICompletionsCompat{OpenRouterRouting: &OpenRouterRouting{AllowFallbacks: &no, RequireParameters: &no, DataCollection: "deny", ZDR: &no, EnforceDistillableText: &no, Order: []string{"a"}, Only: []string{"b"}, Ignore: []string{"c"}, Quantizations: []string{"fp16"}, Sort: map[string]any{"by": "latency"}, MaxPrice: map[string]any{"input": 0}, PreferredMinThroughput: 10, PreferredMaxLatency: map[string]any{"p90": 2}}}, `{"provider":{"allow_fallbacks":false,"require_parameters":false,"data_collection":"deny","zdr":false,"enforce_distillable_text":false,"order":["a"],"only":["b"],"ignore":["c"],"quantizations":["fp16"],"sort":{"by":"latency"},"max_price":{"input":0},"preferred_min_throughput":10,"preferred_max_latency":{"p90":2}}}`},
		{"vercel", &OpenAICompletionsCompat{VercelGatewayRouting: &VercelGatewayRouting{Only: []string{"a"}, Order: []string{"b"}}}, `{"providerOptions":{"gateway":{"only":["a"],"order":["b"]}}}`},
		{"absent", &OpenAICompletionsCompat{}, `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := Model{ID: "test", API: "openai-completions", Provider: "custom-proxy", Compat: tc.compat}
			data, err := json.Marshal(buildOpenAICompletionsRequest(model, Context{}, ProviderStreamOptions{}))
			if err != nil {
				t.Fatal(err)
			}
			var got, want map[string]json.RawMessage
			json.Unmarshal(data, &got)
			json.Unmarshal([]byte(tc.want), &want)
			for _, key := range []string{"provider", "providerOptions"} {
				var g, w any
				if got[key] != nil {
					json.Unmarshal(got[key], &g)
				}
				if want[key] != nil {
					json.Unmarshal(want[key], &w)
				}
				gb, _ := json.Marshal(g)
				wb, _ := json.Marshal(w)
				if string(gb) != string(wb) {
					t.Fatalf("%s: got %s, want %s", key, gb, wb)
				}
			}
		})
	}
}

func TestOpenAICompletionsSamplingCannotOverrideRouting(t *testing.T) {
	for _, key := range []string{"provider", "providerOptions"} {
		t.Run(key, func(t *testing.T) {
			model := Model{ID: "test", API: "openai-completions", Compat: &OpenAICompletionsCompat{OpenRouterRouting: &OpenRouterRouting{Only: []string{"allowed"}}}}
			request := buildOpenAICompletionsRequest(model, Context{}, ProviderStreamOptions{})
			request.SamplingParams = SamplingParams{key: map[string]any{}}
			if _, err := json.Marshal(request); err == nil {
				t.Fatal("routing override accepted")
			}
		})
	}
}

func TestOpenAICompletionsRoutingTransportIsolation(t *testing.T) {
	received := make(chan map[string]json.RawMessage, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		received <- body
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	for _, kind := range []string{"openrouter", "vercel", "plain"} {
		model := Model{ID: "test", API: "openai-completions", Provider: "custom", BaseURL: server.URL}
		switch kind {
		case "openrouter":
			model.Compat = &OpenAICompletionsCompat{OpenRouterRouting: &OpenRouterRouting{Only: []string{"private"}}}
		case "vercel":
			model.Compat = &OpenAICompletionsCompat{VercelGatewayRouting: &VercelGatewayRouting{Only: []string{"private"}}}
		}
		result := streamOpenAICompletions(model, Context{}, ProviderStreamOptions{APIKey: "fixture"}).Result()
		if result.StopReason != StopReasonStop {
			t.Fatalf("%s: %v", kind, result)
		}
		body := <-received
		if (body["provider"] != nil) != (kind == "openrouter") || (body["providerOptions"] != nil) != (kind == "vercel") {
			t.Fatalf("%s: unexpected routing %s", kind, body)
		}
	}
}
