package pigo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// The next wire segment is unavailable until the preceding text was observed.
// A collect-everything-then-replay implementation fails without timing heuristics.
type kiroObserverGatedReader struct {
	first     *bytes.Reader
	tail      *bytes.Reader
	unlocked  bool
	tailReads int
}

func (r *kiroObserverGatedReader) Read(p []byte) (int, error) {
	if r.first.Len() > 0 {
		return r.first.Read(p)
	}
	r.tailReads++
	if !r.unlocked {
		return 0, errors.New("test: read ahead of observer")
	}
	return r.tail.Read(p)
}

func TestKiroIncrementalTextBeforeNextWireSegment(t *testing.T) {
	first := kiroOfflineEvent("assistantResponseEvent", `{"content":"海"}`)
	tail := kiroOfflineJoin(kiroOfflineEvent("assistantResponseEvent", `{"content":"海!"}`), kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`))
	reader := &kiroObserverGatedReader{first: bytes.NewReader(first), tail: bytes.NewReader(tail)}
	var seen []string
	got, err := collectKiroResponseWithTextObserver(context.Background(), reader, kiroOfflineTestLimits(), func(text string) error {
		if len(seen) == 0 && reader.tailReads != 0 {
			t.Fatal("first callback happened after next read")
		}
		seen = append(seen, text)
		reader.unlocked = true
		return nil
	})
	if err != nil || got.StopReason != StopReasonStop || !reflect.DeepEqual(seen, []string{"海", "海!"}) || len(got.Parts) != 2 {
		t.Fatal("incremental text failed", err)
	}
	buffered, err := collectKiroOfflineResponse(context.Background(), bytes.NewReader(kiroOfflineJoin(first, tail)), kiroOfflineTestLimits())
	if err != nil || !reflect.DeepEqual(buffered, got) {
		t.Fatal("observer changed final evidence")
	}
}

func TestKiroIncrementalTextLateFailureKeepsFinalResultEmpty(t *testing.T) {
	text := kiroOfflineEvent("assistantResponseEvent", `{"content":"provisional"}`)
	end := kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`)
	bad := append([]byte(nil), end...)
	bad[len(bad)-1] ^= 1
	for _, tail := range [][]byte{nil, bad, end[:len(end)-1], kiroOfflineFrame("exception", "ThrottlingException", "opaque"), kiroOfflineJoin(end, kiroOfflineEvent("metadataEvent", `{"stopReason":"MAX_TOKENS"}`))} {
		var seen []string
		got, err := collectKiroResponseWithTextObserver(context.Background(), bytes.NewReader(kiroOfflineJoin(text, tail)), kiroOfflineTestLimits(), func(s string) error { seen = append(seen, s); return nil })
		if err == nil || !reflect.DeepEqual(got, kiroOfflineResponse{}) || !reflect.DeepEqual(seen, []string{"provisional"}) {
			t.Fatal("late failure fabricated success or hid provisional delivery")
		}
	}
}

func TestKiroIncrementalTextValidatesBeforeObserving(t *testing.T) {
	frame := kiroOfflineEvent("assistantResponseEvent", `{"content":"x"}`)
	bad := append([]byte(nil), frame...)
	bad[len(bad)-1] ^= 1
	for _, wire := range [][]byte{bad, frame[:len(frame)-1], kiroOfflineEvent("assistantResponseEvent", `{"content":"x","content":"y"}`), kiroOfflineEvent("assistantResponseEvent", `{"content":"\ud800"}`), kiroOfflineJoin(kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`), frame)} {
		calls := 0
		got, err := collectKiroResponseWithTextObserver(context.Background(), bytes.NewReader(wire), kiroOfflineTestLimits(), func(string) error { calls++; return nil })
		if err == nil || calls != 0 || !reflect.DeepEqual(got, kiroOfflineResponse{}) {
			t.Fatal("unvalidated text observed")
		}
	}
	limits := kiroOfflineTestLimits()
	limits.TextBytes = 1
	calls := 0
	_, err := collectKiroResponseWithTextObserver(context.Background(), bytes.NewReader(kiroOfflineEvent("assistantResponseEvent", `{"content":"too long"}`)), limits, func(string) error { calls++; return nil })
	if err == nil || calls != 0 {
		t.Fatal("budget bypassed observer")
	}
}

func TestKiroIncrementalTextObserverErrorAndCancellation(t *testing.T) {
	for _, cancelInside := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		reader := &kiroObserverGatedReader{first: bytes.NewReader(kiroOfflineEvent("assistantResponseEvent", `{"content":"preview"}`)), tail: bytes.NewReader(nil)}
		calls := 0
		got, err := collectKiroResponseWithTextObserver(ctx, reader, kiroOfflineTestLimits(), func(string) error {
			calls++
			if cancelInside {
				cancel()
				return nil
			}
			return errors.New("private-observer-detail")
		})
		cancel()
		if err == nil || calls != 1 || reader.tailReads != 0 || !reflect.DeepEqual(got, kiroOfflineResponse{}) || strings.Contains(err.Error(), "private-observer-detail") {
			t.Fatal("callback failure leaked or continued")
		}
		if cancelInside && !errors.Is(err, errKiroOfflineCancelled) {
			t.Fatal("cancellation classification lost")
		}
		if !cancelInside && !errors.Is(err, errKiroTextObserver) {
			t.Fatal("observer failure classification lost")
		}
	}
}

func TestKiroIncrementalTextNeverObservesToolOrReasoning(t *testing.T) {
	frames := [][]byte{kiroOfflineEvent("reasoningContentEvent", `{"text":"private thought","signature":"opaque"}`), kiroOfflineEvent("assistantResponseEvent", `{"content":"visible"}`)}
	frames = append(frames, kiroOfflineToolFrames()...)
	frames = append(frames, kiroOfflineEvent("metadataEvent", `{"stopReason":"TOOL_USE"}`))
	var seen []string
	got, err := collectKiroResponseWithTextObserver(context.Background(), bytes.NewReader(kiroOfflineJoin(frames...)), kiroOfflineTestLimits(), func(s string) error { seen = append(seen, s); return nil })
	if err != nil || got.StopReason != StopReasonToolUse || len(got.Parts) != 3 || got.Parts[2].Tool == nil || !reflect.DeepEqual(seen, []string{"visible"}) {
		t.Fatal("nontext exposed or final tools lost", err)
	}
	// Reader ownership is unchanged; even a closable reader is not closed here.
	body := &kiroObserverCloseProbe{Reader: bytes.NewReader(kiroOfflineJoin(frames...))}
	_, err = collectKiroResponseWithTextObserver(context.Background(), body, kiroOfflineTestLimits(), nil)
	if err != nil || body.closed {
		t.Fatal("collector changed reader ownership")
	}
}

type kiroObserverCloseProbe struct {
	io.Reader
	closed bool
}

func (r *kiroObserverCloseProbe) Close() error { r.closed = true; return nil }
