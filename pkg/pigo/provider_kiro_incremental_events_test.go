package pigo

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
)

func kiroMapperFixture(t *testing.T) (*kiroIncrementalTextMapper, kiroOfflineResponse) {
	t.Helper()
	response, model, stamp := kiroTextMessageFixture()
	m, err := newKiroIncrementalTextMapper(model, stamp, 4096, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return m, response
}
func kiroMapperCost(events []AssistantMessageEvent) int {
	n := 0
	for _, e := range events {
		n += assistantEventBytes(e)
	}
	return n
}

func TestKiroIncrementalMapperLifecycleCoalescingAndOwnership(t *testing.T) {
	m, response := kiroMapperFixture(t)
	first, err := m.accept("fi")
	if err != nil || len(first) != 3 || first[0].Type != AssistantMessageEventStart || len(first[0].Partial.Content) != 0 || first[1].Type != AssistantMessageEventTextStart || first[1].Partial.Content[0].(TextContent).Text != "" || first[2].Delta != "fi" {
		t.Fatal("first lifecycle invalid", err)
	}
	second, err := m.accept("rst second")
	if err != nil || len(second) != 1 || second[0].ContentIndex != 0 || second[0].Partial.Content[0].(TextContent).Text != "first second" {
		t.Fatal("incremental accumulation invalid", err)
	}
	first[2].Partial.Content[0] = TextContent{Text: "caller edit"}
	response.Usage.TokenCounts = kiroCompleteCounts()
	final, err := m.complete(response)
	if err != nil || len(final) != 2 || final[0].Type != AssistantMessageEventTextEnd || final[0].Content != "first second" || final[1].Type != AssistantMessageEventDone || final[1].Reason != StopReasonStop || len(final[1].Message.Content) != 1 || final[1].Message.Content[0].(TextContent).Text != "first second" || !final[1].Message.UsageReported {
		t.Fatal("final evidence mismatch", err)
	}
	if second[0].Partial.UsageReported || second[0].Partial.StopReason != "" || final[0].Partial.UsageReported {
		t.Fatal("future usage or terminal leaked")
	}
	final[1].Message.Content[0] = TextContent{Text: "edited done"}
	if final[0].Partial.Content[0].(TextContent).Text != "first second" {
		t.Fatal("terminal snapshots alias")
	}
	if _, ok := m.fail(false); ok {
		t.Fatal("error after done")
	}
	if out, err := m.accept("late"); err == nil || out != nil {
		t.Fatal("accepted after done")
	}
	if out, err := m.complete(response); err == nil || out != nil {
		t.Fatal("second done")
	}
}

func TestKiroIncrementalMapperUnsupportedFinalAndStickyFailure(t *testing.T) {
	for _, mutate := range []func(*kiroOfflineResponse){
		func(r *kiroOfflineResponse) { r.Parts[0].Text = "different" },
		func(r *kiroOfflineResponse) { r.Parts[0].Kind = "tool" },
		func(r *kiroOfflineResponse) { r.Parts[0].Kind = "reasoning" },
		func(r *kiroOfflineResponse) { r.Parts[0].Kind = "context_usage" },
		func(r *kiroOfflineResponse) { r.Usage.TokenCounts = map[string]int64{"outputTokens": 1} },
		func(r *kiroOfflineResponse) { r.Usage.StopReason = "" },
	} {
		m, response := kiroMapperFixture(t)
		if _, err := m.accept("first second"); err != nil {
			t.Fatal(err)
		}
		mutate(&response)
		if out, err := m.complete(response); err == nil || out != nil {
			t.Fatal("unsupported final accepted")
		}
		if out, err := m.accept("more"); err == nil || out != nil {
			t.Fatal("failure not sticky")
		}
		valid, _, _ := kiroTextMessageFixture()
		if out, err := m.complete(valid); err == nil || out != nil {
			t.Fatal("recovered into success")
		}
		event, ok := m.fail(false)
		if !ok || event.Type != AssistantMessageEventError || event.Reason != StopReasonError {
			t.Fatal("missing error terminal")
		}
		if _, ok := m.fail(true); ok {
			t.Fatal("duplicate terminal")
		}
	}
}

func TestKiroIncrementalMapperAtomicBudgetAndErrorReserve(t *testing.T) {
	m, response := kiroMapperFixture(t)
	first, err := m.accept("first second")
	if err != nil {
		t.Fatal(err)
	}
	final, err := m.complete(response)
	if err != nil {
		t.Fatal(err)
	}
	_, model, stamp := kiroTextMessageFixture()
	reserve := assistantEventBytes(m.failureEvent(true))
	for _, budget := range []int{reserve + kiroMapperCost(first) - 1, reserve + kiroMapperCost(first) + kiroMapperCost(final) - 1} {
		limited, err := newKiroIncrementalTextMapper(model, stamp, 4096, budget)
		if err != nil {
			t.Fatal(err)
		}
		before := limited.remainingBytes
		out, err := limited.accept("first second")
		if budget == reserve+kiroMapperCost(first)-1 {
			if err == nil || out != nil || limited.fragments != 0 || limited.remainingBytes != before {
				t.Fatal("partial batch committed")
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			before = limited.remainingBytes
			if out, err := limited.complete(response); err == nil || out != nil || limited.remainingBytes != before {
				t.Fatal("partial final batch committed")
			}
		}
		e, ok := limited.fail(true)
		if !ok || e.Reason != StopReasonAborted {
			t.Fatal("reserved terminal unavailable")
		}
	}
}

func TestKiroIncrementalMapperEmptyFragmentsLimitsAndEarlyFail(t *testing.T) {
	m, _ := kiroMapperFixture(t)
	event, ok := m.fail(true)
	if !ok || event.Reason != StopReasonAborted || len(event.Error.Content) != 0 {
		t.Fatal("early failure invalid")
	}
	_, model, stamp := kiroTextMessageFixture()
	m, err := newKiroIncrementalTextMapper(model, stamp, 4096, assistantMessageEventByteBudget)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < assistantMessageEventDeltaBuffer; i++ {
		if _, err := m.accept(""); err != nil {
			t.Fatal("empty fragment count rejected early", i, err)
		}
	}
	if out, err := m.accept(""); err == nil || out != nil {
		t.Fatal("empty fragment bypassed cap")
	}
	m, _ = kiroMapperFixture(t)
	if out, err := m.accept(string([]byte{255})); err == nil || out != nil {
		t.Fatal("invalid UTF8 accepted")
	}
	m, response := kiroMapperFixture(t)
	response.StopReason = StopReasonLength
	response.Usage.StopReason = "MAX_TOKENS"
	if _, err := m.accept("first second"); err != nil {
		t.Fatal(err)
	}
	final, err := m.complete(response)
	if err != nil || final[1].Reason != StopReasonLength || final[1].Message.UsageReported {
		t.Fatal("length/unknown usage lost", err)
	}
}

func TestKiroIncrementalMapperThroughInjectedHTTP(t *testing.T) {
	for _, lateFailure := range []bool{false, true} {
		m, _ := kiroMapperFixture(t)
		first := kiroOfflineEvent("assistantResponseEvent", `{"content":"live fragment"}`)
		gated := &kiroObserverGatedReader{first: bytes.NewReader(first), tail: bytes.NewReader(kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`))}
		body := &kiroAttemptBody{reader: gated}
		if lateFailure {
			body.closeErr = errors.New("private-close")
		}
		var events []AssistantMessageEvent
		result, err := kiroRunObservedHTTPFixture(context.Background(), func(*http.Request) (*http.Response, error) { return kiroAttemptResponse(body), nil }, func(text string) error {
			batch, err := m.accept(text)
			if err != nil {
				return err
			}
			events = append(events, batch...)
			if len(events) != 3 || events[2].Type != AssistantMessageEventTextDelta || gated.tailReads != 0 {
				t.Fatal("host delta emitted too late")
			}
			gated.unlocked = true
			return nil
		})
		if err != nil {
			if !lateFailure || !reflect.DeepEqual(result, kiroOfflineResponse{}) {
				t.Fatal("unexpected HTTP outcome")
			}
			terminal, ok := m.fail(false)
			if !ok {
				t.Fatal("missing late error")
			}
			events = append(events, terminal)
		} else {
			batch, err := m.complete(result)
			if err != nil {
				t.Fatal(err)
			}
			events = append(events, batch...)
		}
		want := AssistantMessageEventDone
		if lateFailure {
			want = AssistantMessageEventError
		}
		if events[len(events)-1].Type != want || body.closes != 1 {
			t.Fatal("late failure became Done")
		}
	}
}

func TestKiroIncrementalMapperConstructorAndTextBudget(t *testing.T) {
	_, model, stamp := kiroTextMessageFixture()
	for _, limits := range [][2]int{{0, 1 << 20}, {1<<20 + 1, 1 << 20}, {4096, 0}, {4096, assistantMessageEventByteBudget + 1}, {4096, 1}} {
		if m, err := newKiroIncrementalTextMapper(model, stamp, limits[0], limits[1]); err == nil || m != nil {
			t.Fatal("invalid limits admitted")
		}
	}
	bad := model
	bad.ID = ""
	if m, err := newKiroIncrementalTextMapper(bad, stamp, 4096, 1<<20); err == nil || m != nil {
		t.Fatal("invalid identity admitted")
	}
	m, err := newKiroIncrementalTextMapper(model, stamp, 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.accept("a"); err != nil {
		t.Fatal(err)
	}
	if out, err := m.accept("b"); err == nil || out != nil {
		t.Fatal("cumulative text budget ignored")
	}
	if _, ok := m.fail(false); !ok {
		t.Fatal("no terminal after budget failure")
	}
	m, _ = newKiroIncrementalTextMapper(model, stamp, 1, 1<<20)
	if _, err := m.accept(""); err != nil {
		t.Fatal(err)
	}
	response := kiroOfflineResponse{Parts: []kiroOfflinePart{{Kind: "text"}}, StopReason: StopReasonStop, Usage: kiroMetadataSnapshot{StopReason: "END_TURN"}}
	if out, err := m.complete(response); err == nil || out != nil {
		t.Fatal("all-empty final accepted")
	}
}
