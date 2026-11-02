package pigo

import (
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func kiroTextMessageFixture() (kiroOfflineResponse, Model, time.Time) {
	return kiroOfflineResponse{Parts: []kiroOfflinePart{{Kind: "text", Text: "first"}, {Kind: "text", Text: " second"}}, StopReason: StopReasonStop, Usage: kiroMetadataSnapshot{StopReason: "END_TURN"}}, Model{ID: "fixture-model", API: "fixture-kiro-api", Provider: "fixture-kiro"}, time.Unix(123, 0).UTC()
}
func kiroCompleteCounts() map[string]int64 {
	return map[string]int64{"uncachedInputTokens": 3, "outputTokens": 7, "cacheReadInputTokens": 1, "cacheWriteInputTokens": 2, "totalTokens": 99}
}

func TestKiroCompletedTextIdentityOrderAndUsagePresence(t *testing.T) {
	response, model, stamp := kiroTextMessageFixture()
	message, err := projectKiroCompletedText(response, model, stamp, 1024)
	if err != nil || len(message.Content) != 2 || message.Content[0].(TextContent).Text != "first" || message.Content[1].(TextContent).Text != " second" || message.Model != model.ID || message.API != model.API || message.Provider != model.Provider || !message.Timestamp.Equal(stamp) || message.UsageReported || message.StopReason != StopReasonStop {
		t.Fatal("text identity/order/unknown-usage changed")
	}
	response.Usage.TokenCounts = kiroCompleteCounts()
	message, err = projectKiroCompletedText(response, model, stamp, 1024)
	if err != nil || !message.UsageReported || message.Usage.Input != 3 || message.Usage.Output != 7 || message.Usage.CacheRead != 1 || message.Usage.CacheWrite != 2 || message.Usage.TotalTokens != 99 || message.Usage.Cost != (UsageCost{}) {
		t.Fatal("usage recomputed or invented")
	}
	for name := range response.Usage.TokenCounts {
		response.Usage.TokenCounts[name] = 0
	}
	message, err = projectKiroCompletedText(response, model, stamp, 1024)
	if err != nil || !message.UsageReported || message.Usage != (Usage{}) {
		t.Fatal("reported zero collapsed into absent usage")
	}
	response.StopReason = StopReasonLength
	response.Usage.StopReason = "MAX_TOKENS"
	message, err = projectKiroCompletedText(response, model, stamp, 1024)
	if err != nil || message.StopReason != StopReasonLength {
		t.Fatal("explicit truncation lost")
	}
}

func TestKiroCompletedTextRejectsUnsupportedEvidence(t *testing.T) {
	for _, mutate := range []func(*kiroOfflineResponse){
		func(r *kiroOfflineResponse) { r.Parts = nil },
		func(r *kiroOfflineResponse) { r.Parts = []kiroOfflinePart{{Kind: "text"}} },
		func(r *kiroOfflineResponse) { r.Parts[0].Kind = "tool" },
		func(r *kiroOfflineResponse) { r.Parts[0].Kind = "reasoning" },
		func(r *kiroOfflineResponse) { r.Parts[0].Kind = "context_usage" },
		func(r *kiroOfflineResponse) { r.Parts[0].Kind = "future-kind" },
		func(r *kiroOfflineResponse) { r.Parts[0].Signature = "opaque-signature" },
		func(r *kiroOfflineResponse) { v := 50.0; r.Parts[0].ContextUsagePercentage = &v },
		func(r *kiroOfflineResponse) { r.Parts[0].Tool = &kiroAssembledTool{} },
		func(r *kiroOfflineResponse) { r.Parts[0].Text = string([]byte{255}) },
		func(r *kiroOfflineResponse) { r.StopReason = StopReasonError },
		func(r *kiroOfflineResponse) { r.StopReason = StopReasonAborted },
		func(r *kiroOfflineResponse) { r.StopReason = StopReasonToolUse },
		func(r *kiroOfflineResponse) { r.Usage.StopReason = "" },
		func(r *kiroOfflineResponse) { r.Usage.StopReason = "MAX_TOKENS" },
		func(r *kiroOfflineResponse) { v := 50.0; r.Usage.ContextUsagePercentage = &v },
		func(r *kiroOfflineResponse) { v := 1.0; r.Usage.NormalizedTokenUsage = &v },
		func(r *kiroOfflineResponse) { r.Usage.TokenCounts = map[string]int64{"outputTokens": 1} },
		func(r *kiroOfflineResponse) {
			r.Usage.TokenCounts = kiroCompleteCounts()
			delete(r.Usage.TokenCounts, "totalTokens")
			r.Usage.TokenCounts["other"] = 9
		},
		func(r *kiroOfflineResponse) {
			r.Usage.TokenCounts = kiroCompleteCounts()
			r.Usage.TokenCounts["outputTokens"] = -1
		},
	} {
		response, model, stamp := kiroTextMessageFixture()
		mutate(&response)
		message, err := projectKiroCompletedText(response, model, stamp, 1024)
		if err == nil || !reflect.DeepEqual(message, AssistantMessage{}) {
			t.Fatal("unsupported evidence silently dropped or partially returned")
		}
	}
}

func TestKiroCompletedTextIntWidthAndOwnership(t *testing.T) {
	response, model, stamp := kiroTextMessageFixture()
	response.Usage.TokenCounts = kiroCompleteCounts()
	response.Usage.TokenCounts["totalTokens"] = math.MaxInt64
	message, err := projectKiroCompletedText(response, model, stamp, 1024)
	if strconv.IntSize == 32 {
		if err == nil || !reflect.DeepEqual(message, AssistantMessage{}) {
			t.Fatal("int64 overflow accepted")
		}
	} else if err != nil || int64(message.Usage.TotalTokens) != math.MaxInt64 {
		t.Fatal("representable count lost")
	}
	response.Usage.TokenCounts = kiroCompleteCounts()
	message, err = projectKiroCompletedText(response, model, stamp, 1024)
	if err != nil {
		t.Fatal(err)
	}
	message.Content[0] = TextContent{Text: "changed"}
	message.Usage.Input = 100
	if response.Parts[0].Text != "first" || response.Usage.TokenCounts["uncachedInputTokens"] != 3 {
		t.Fatal("host projection aliases evidence")
	}
	response.Parts[1].Text = "mutated"
	if message.Content[1].(TextContent).Text != " second" {
		t.Fatal("evidence mutation changed message")
	}
}

func TestKiroCompletedTextBudgetsAndIdentity(t *testing.T) {
	response, model, stamp := kiroTextMessageFixture()
	for _, budget := range []int{0, 10, 1<<20 + 1} {
		if message, err := projectKiroCompletedText(response, model, stamp, budget); err == nil || !reflect.DeepEqual(message, AssistantMessage{}) {
			t.Fatal("invalid text budget accepted")
		}
	}
	if _, err := projectKiroCompletedText(response, model, stamp, 12); err != nil {
		t.Fatal("exact text budget rejected")
	}
	response.Parts = make([]kiroOfflinePart, 4097)
	if message, err := projectKiroCompletedText(response, model, stamp, 1024); err == nil || !reflect.DeepEqual(message, AssistantMessage{}) {
		t.Fatal("part cap ignored")
	}
	for _, mutate := range []func(*Model){func(m *Model) { m.ID = "" }, func(m *Model) { m.API = "" }, func(m *Model) { m.Provider = "" }, func(m *Model) { m.ID = strings.Repeat("x", 257) }} {
		response, model, stamp = kiroTextMessageFixture()
		mutate(&model)
		if message, err := projectKiroCompletedText(response, model, stamp, 1024); err == nil || !reflect.DeepEqual(message, AssistantMessage{}) {
			t.Fatal("invalid identity accepted")
		}
	}
}
