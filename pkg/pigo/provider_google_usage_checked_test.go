package pigo

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestGoogleUsageCheckedArithmetic(t *testing.T) {
	for _, usage := range []googleUsageMetadata{
		{CandidatesTokenCount: math.MaxInt, ThoughtsTokenCount: 1},
		{PromptTokenCount: 8, CachedContentTokenCount: 9},
		{PromptTokenCount: -1}, {CandidatesTokenCount: -1}, {ThoughtsTokenCount: -1}, {CachedContentTokenCount: -1}, {TotalTokenCount: -1},
	} {
		before := AssistantMessage{Usage: Usage{Input: 2, Output: 3, TotalTokens: 5}, UsageReported: true}
		after := before
		if err := applyGoogleUsage(&after, Model{}, usage); err == nil {
			t.Fatalf("accepted invalid usage %+v", usage)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatal("invalid usage replaced prior snapshot")
		}
	}
	for _, usage := range []googleUsageMetadata{
		{}, {PromptTokenCount: math.MaxInt, CachedContentTokenCount: math.MaxInt},
		{CandidatesTokenCount: math.MaxInt - 1, ThoughtsTokenCount: 1, TotalTokenCount: 3},
	} {
		var response AssistantMessage
		if err := applyGoogleUsage(&response, Model{}, usage); err != nil || !response.UsageReported {
			t.Fatalf("valid boundary rejected: %v", err)
		}
	}
}

func TestGoogleInvalidUsageHTTPPreservesPartial(t *testing.T) {
	for _, priorUsage := range []bool{false, true} {
		t.Run(fmt.Sprint(priorUsage), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				prior := ""
				if priorUsage {
					prior = `,"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"totalTokenCount":5}`
				}
				fmt.Fprintf(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial answer\"}]}}]%s}\n\n", prior)
				w.(http.Flusher).Flush()
				fmt.Fprintf(w, "data: {\"usageMetadata\":{\"candidatesTokenCount\":%d,\"thoughtsTokenCount\":1}}\n\n", math.MaxInt)
			}))
			defer server.Close()
			stream := Stream(Model{ID: "fixture", API: "google-generative-ai", Provider: "google", BaseURL: server.URL}, Context{}, ProviderStreamOptions{APIKey: "fixture", MaxRetries: 2})
			done, fail := 0, 0
			for e := range stream.Events() {
				if e.Type == AssistantMessageEventDone {
					done++
				}
				if e.Type == AssistantMessageEventError {
					fail++
				}
			}
			result := stream.Result()
			failure, ok := ProviderFailureOf(result)
			if result.StopReason != StopReasonError || !ok || failure.Kind != ProviderFailureParse || failure.Reason != "invalid_usage" || len(result.Content) != 1 || result.Content[0].(TextContent).Text != "partial answer" || result.UsageReported != priorUsage || requests.Load() != 1 || done != 0 || fail != 1 {
				t.Fatalf("result=%+v failure=%+v requests=%d done=%d fail=%d", result, failure, requests.Load(), done, fail)
			}
			if priorUsage && result.Usage.TotalTokens != 5 {
				t.Fatal("lost earlier valid usage")
			}
		})
	}
}
