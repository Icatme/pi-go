package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestAnthropicHostedUsageAggregateOverflow(t *testing.T) {
	count := int(^uint(0)>>1)/2 + 3
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, buildAnthropicHostedUsageRoundSSE(fmt.Sprint("msg", n), fmt.Sprint("call", n), count, 0, true, n >= 3))
	}))
	defer s.Close()
	m := GetModel("kimi-coding", "k2p5")
	m.BaseURL = s.URL
	m.Cost = UsageCost{Input: 1}
	m.CostTiers = nil
	stream := Stream(*m, Context{Messages: []Message{UserMessage{Content: "Search"}}, HostedTools: []HostedTool{{Type: HostedToolTypeWebSearch, Name: "web_search"}}}, ProviderStreamOptions{APIKey: "fixture"})
	events := collectAssistantMessageEvents(stream.Events())
	r := stream.Result()
	f, ok := ProviderFailureOf(r)
	if requests.Load() != 2 || r.StopReason != StopReasonError || !ok || f.Kind != ProviderFailureParse || f.Reason != "invalid_usage_aggregate" {
		t.Fatalf("requests=%d result=%+v failure=%+v", requests.Load(), r, f)
	}
	if !r.UsageReported || r.Usage.Input != count || r.Usage.TotalTokens != count {
		t.Fatal("lost prior aggregate", r.Usage)
	}

	if len(r.HostedToolExecutions) != 1 || r.HostedToolExecutions[0].ID != "call1" {
		t.Fatal("lost previous execution or executed current continuation", r.HostedToolExecutions)
	}
	if len(r.Content) != 1 {
		t.Fatal("lost current partial", r.Content)
	}
	done, terminalError := 0, 0
	for _, event := range events {
		if event.Type == AssistantMessageEventDone {
			done++
		}
		if event.Type == AssistantMessageEventError {
			terminalError++
		}
	}
	if done != 0 || terminalError != 1 {
		t.Fatalf("done=%d error=%d", done, terminalError)
	}
	raw, err := json.Marshal(Context{Messages: []Message{cloneAssistantMessage(r)}})
	if err != nil {
		t.Fatal(err)
	}
	var restored Context
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	rr := restored.Messages[0].(AssistantMessage)
	if restoredFailure, ok := ProviderFailureOf(rr); !ok || restoredFailure != f {
		t.Fatal("failure lost on restore", restoredFailure)
	}
	for _, message := range []AssistantMessage{r, rr} {
		found := 0
		for _, d := range message.Diagnostics {
			if d.Type != "anthropic_usage_aggregate" {
				continue
			}
			found++
			if d.Details["usage_scope"] != "previous_requests_only" || len(d.Details) != 3 {
				t.Fatal(d)
			}
			for _, key := range []string{"previous_usage", "request_usage"} {
				evidence, ok := d.Details[key].(map[string]any)
				if !ok || len(evidence) != 10 || evidence["input"] != strconv.Itoa(count) || evidence["total"] != strconv.Itoa(count) {
					t.Fatalf("lost exact evidence: %+v", d)
				}
				if evidence["cost_input"] != strconv.FormatFloat(r.Usage.Cost.Input, 'g', -1, 64) {
					t.Fatal("changed original cost evidence", evidence)
				}
			}
			encoded, _ := json.Marshal(d.Details)
			if len(encoded) > 2048 {
				t.Fatal("unbounded diagnostics", len(encoded))
			}
		}
		if found != 1 {
			t.Fatal("missing aggregate evidence", message.Diagnostics)
		}
	}
}

func TestAnthropicHostedUsageCheckedAddition(t *testing.T) {
	max := int(^uint(0) >> 1)
	for _, field := range []string{"input", "output", "read", "write", "total"} {
		a, b := Usage{}, Usage{}
		switch field {
		case "input":
			a.Input = max
			b.Input = 1
		case "output":
			a.Output = max
			b.Output = 1
		case "read":
			a.CacheRead = max
			b.CacheRead = 1
		case "write":
			a.CacheWrite = max
			b.CacheWrite = 1
		case "total":
			a.TotalTokens = max
			b.TotalTokens = 1
		}
		got, err := addAnthropicHostedToolUsage(a, b)
		if err == nil || !reflect.DeepEqual(got, a) {
			t.Fatalf("%s changed prior aggregate %+v err=%v", field, got, err)
		}
	}
	a := Usage{Input: max - 1, TotalTokens: max - 1, Cost: UsageCost{Input: 2, Total: 2}}
	b := Usage{Input: 1, TotalTokens: 1, Cost: UsageCost{Input: 3, Total: 3}}
	got, err := addAnthropicHostedToolUsage(a, b)
	if err != nil || got.Input != max || got.TotalTokens != max || got.Cost.Total != 5 {
		t.Fatal(got, err)
	}
	for _, pair := range [][2]Usage{{{Input: -1}, {}}, {{}, {CacheRead: -1}}} {
		got, err := addAnthropicHostedToolUsage(pair[0], pair[1])
		if err == nil || got != pair[0] {
			t.Fatal(got, err)
		}
	}
}
