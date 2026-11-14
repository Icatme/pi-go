package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestAnthropicInvalidUsageStream(t *testing.T) {
	max := int(^uint(0) >> 1)
	for _, phase := range []string{"start", "delta"} {
		for _, field := range []string{"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "overflow"} {
			t.Run(phase+"/"+field, func(t *testing.T) {
				bad := map[string]int{field: -1}
				if field == "overflow" {
					bad = map[string]int{"input_tokens": max, "output_tokens": 1}
				}
				raw, _ := json.Marshal(bad)
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					if phase == "start" {
						fmt.Fprintf(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":%s}}\n\n", raw)
					} else {
						fmt.Fprint(w, `data: {"type":"message_start","message":{"usage":{"input_tokens":7,"output_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"partial"}}

data: {"type":"content_block_stop","index":0}

`)
						fmt.Fprintf(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":%s}\n\n", raw)
					}
					fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
				}))
				defer server.Close()
				m := Model{ID: "requested", Provider: "anthropic", API: "anthropic-messages", BaseURL: server.URL, Cost: UsageCost{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4}}
				s := Stream(m, Context{}, ProviderStreamOptions{APIKey: "fixture"})
				events := collectAssistantMessageEvents(s.Events())
				r := s.Result()
				failure, ok := ProviderFailureOf(r)
				if r.StopReason != StopReasonError || !ok || failure.Kind != ProviderFailureParse || failure.Reason != "invalid_usage" {
					t.Fatalf("result=%+v failure=%+v", r, failure)
				}
				if requests.Load() != 1 || r.Model != m.ID {
					t.Fatal("request contract changed", requests.Load(), r.Model)
				}
				if phase == "start" {
					if r.UsageReported || r.Usage != (Usage{}) {
						t.Fatal("accepted invalid initial usage", r.Usage)
					}
				} else {
					want := Usage{Input: 7, Output: 2, CacheRead: 3, CacheWrite: 4, TotalTokens: 16}
					want.Cost = CalculateCost(m, want)
					if !r.UsageReported || !reflect.DeepEqual(r.Usage, want) || len(r.Content) != 1 || r.Content[0].(TextContent).Text != "partial" {
						t.Fatalf("lost valid snapshot/partial: %+v", r)
					}
				}
				done, errors := 0, 0
				for _, e := range events {
					if e.Type == AssistantMessageEventDone {
						done++
					}
					if e.Type == AssistantMessageEventError {
						errors++
					}
				}
				if done != 0 || errors != 1 {
					t.Fatalf("done=%d errors=%d", done, errors)
				}
			})
		}
	}
}

func TestAnthropicUsageValidBoundaries(t *testing.T) {
	max := int(^uint(0) >> 1)
	for _, u := range []anthropicUsage{{}, {InputTokens: max}, {CacheReadTokens: max}, {CacheCreationTokens: max}, {InputTokens: max - 3, OutputTokens: 1, CacheReadTokens: 1, CacheCreationTokens: 1}, {CacheReadTokens: 9, CacheCreationTokens: 7}} {
		var r AssistantMessage
		if err := applyAnthropicUsage(&r, Model{}, u); err != nil || !r.UsageReported || r.Usage.TotalTokens < 0 {
			t.Fatalf("u=%+v result=%+v err=%v", u, r, err)
		}
		if r.Usage.Input != u.InputTokens || r.Usage.CacheRead != u.CacheReadTokens || r.Usage.CacheWrite != u.CacheCreationTokens {
			t.Fatal("changed legal cache semantics", r.Usage)
		}
	}
}

func TestAnthropicUsageDeltaPresenceAndKimiValidation(t *testing.T) {
	for _, provider := range []Provider{"anthropic", "kimi-coding"} {
		m := Model{ID: "requested", Provider: provider, API: "anthropic-messages"}
		r := AssistantMessage{Model: m.ID, Provider: m.Provider, API: m.API}
		states := map[int]*anthropicStreamingBlockState{}
		process := func(data string) error {
			_, err := processAnthropicStreamEvent(data, m, &r, nil, states, false, nil, nil, false)
			return err
		}
		if err := process(`{"type":"message_start","message":{"usage":{"input_tokens":0,"output_tokens":1,"cache_read_input_tokens":9,"cache_creation_input_tokens":7}}}`); err != nil {
			t.Fatal(err)
		}
		if err := process(`{"type":"message_delta","usage":{"output_tokens":2}}`); err != nil {
			t.Fatal(err)
		}
		if r.Usage != (Usage{Input: 0, Output: 2, CacheRead: 9, CacheWrite: 7, TotalTokens: 18}) {
			t.Fatal("missing fields reset previous usage", r.Usage)
		}
		if err := process(`{"type":"message_delta","usage":{"output_tokens":0,"cache_read_input_tokens":0}}`); err != nil {
			t.Fatal(err)
		}
		if r.Usage.Output != 0 || r.Usage.CacheRead != 0 || r.Usage.CacheWrite != 7 || r.Usage.TotalTokens != 7 {
			t.Fatal("explicit zero not applied", r.Usage)
		}
		before := r.Usage
		if err := process(`{"type":"message_delta","usage":{"cache_creation_input_tokens":-1}}`); err == nil {
			t.Fatal("negative Kimi/shared usage accepted")
		}
		if r.Usage != before {
			t.Fatal("invalid frame mutated valid usage", r.Usage)
		}
		if err := process(`{"type":"message_delta"}`); err != nil || r.Usage != before {
			t.Fatal("missing usage altered snapshot", err, r.Usage)
		}
	}
}
