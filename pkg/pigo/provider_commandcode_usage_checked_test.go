package pigo

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestCommandCodeInvalidUsagePreservesSnapshot(t *testing.T) {
	max := int(^uint(0) >> 1)
	for _, value := range []any{-1, 1.5, math.NaN(), math.Inf(1), math.Ldexp(1, 63)} {
		before := AssistantMessage{Usage: Usage{Input: 2, Output: 3, TotalTokens: 5}, UsageReported: true}
		after := before
		err := applyCommandCodeUsage(map[string]any{"totalUsage": map[string]any{"inputTokens": value}}, Model{}, &after)
		if err == nil {
			t.Errorf("invalid %v accepted", value)
		}
		if !reflect.DeepEqual(before, after) {
			t.Errorf("invalid %v mutated snapshot: %+v", value, after.Usage)
		}
	}
	before := AssistantMessage{Usage: Usage{Input: 2}, UsageReported: true}
	after := before
	applyCommandCodeUsage(map[string]any{"totalUsage": map[string]any{"inputTokens": 8, "inputTokenDetails": map[string]any{"cacheReadTokens": max, "cacheWriteTokens": max}}}, Model{}, &after)
	if !reflect.DeepEqual(before, after) {
		t.Error("overflow mutated snapshot", after.Usage)
	}
}

func TestCommandCodeUsageCheckedBoundaries(t *testing.T) {
	max := int(^uint(0) >> 1)
	for _, value := range []any{0, int64(max), float64(2), float32(2), math.Nextafter(math.Ldexp(1, strconv.IntSize-1), 0)} {
		if _, present, err := commandCodeUsageInt(value); err != nil || !present {
			t.Fatalf("valid %v: %v", value, err)
		}
	}
	for _, value := range []any{nil} {
		if _, p, e := commandCodeUsageInt(value); p || e != nil {
			t.Fatal("null changed")
		}
	}
	for _, explicit := range []any{nil, 0} {
		r := AssistantMessage{}
		err := applyCommandCodeUsage(map[string]any{"totalUsage": map[string]any{"inputTokens": 10, "outputTokens": 1, "inputTokenDetails": map[string]any{"noCacheTokens": explicit, "cacheReadTokens": 3}}}, Model{}, &r)
		want := 7
		if explicit != nil {
			want = 0
		}
		if err != nil || r.Usage.Input != want {
			t.Fatal(err, r.Usage)
		}
	}
	var r AssistantMessage
	if err := applyCommandCodeUsage(map[string]any{"totalUsage": map[string]any{"inputTokens": max, "outputTokens": 1}}, Model{}, &r); err == nil {
		t.Fatal("total overflow")
	}
}

func TestCommandCodeInvalidUsageHTTP(t *testing.T) {
	for _, bad := range []string{"-1", "1.5", "9223372036854775808"} {
		t.Run(bad, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				fmt.Fprintln(w, `{"type":"text-delta","text":"partial"}`)
				fmt.Fprintf(w, "{\"type\":\"finish\",\"finishReason\":\"stop\",\"totalUsage\":{\"inputTokens\":%s}}\n", bad)
			}))
			defer server.Close()
			stream := commandCodeGenerateSimpleForTest(commandCodeTestModel(server.URL), Context{}, SimpleStreamOptions{APIKey: "fixture", MaxRetries: 2})
			done, failed := 0, 0
			for e := range stream.Events() {
				if e.Type == AssistantMessageEventDone {
					done++
				}
				if e.Type == AssistantMessageEventError {
					failed++
				}
			}
			r := stream.Result()
			f, ok := ProviderFailureOf(r)
			if !ok || f.Kind != ProviderFailureParse || f.Reason != "invalid_usage" || done != 0 || failed != 1 || requests.Load() != 1 || r.UsageReported || len(r.Content) != 1 || r.Content[0].(TextContent).Text != "partial" {
				t.Fatalf("%+v failure%+v events%d/%d requests%d", r, f, done, failed, requests.Load())
			}
		})
	}
}

func TestCommandCodeUsageAllFieldsChecked(t *testing.T) {
	for _, field := range []string{"inputTokens", "outputTokens", "cacheReadTokens", "cacheWriteTokens", "noCacheTokens"} {
		usage := map[string]any{}
		details := map[string]any{}
		usage["inputTokenDetails"] = details
		if field == "inputTokens" || field == "outputTokens" {
			usage[field] = -1
		} else {
			details[field] = -1
		}
		var r AssistantMessage
		if err := applyCommandCodeUsage(map[string]any{"totalUsage": usage}, Model{}, &r); err == nil || r.UsageReported {
			t.Fatalf("accepted %s", field)
		}
	}
}
