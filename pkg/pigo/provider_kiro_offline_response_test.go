package pigo

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"testing"
)

func kiroOfflineFrame(kind, name, payload string) []byte {
	key := ":event-type"
	if kind == "exception" { key = ":exception-type" }
	var headers []byte
	for _, pair := range [][2]string{{":message-type", kind}, {key, name}, {":content-type", "application/json"}} {
		headers = append(headers, byte(len(pair[0])))
		headers = append(headers, pair[0]...)
		headers = append(headers, 7)
		var size [2]byte
		binary.BigEndian.PutUint16(size[:], uint16(len(pair[1])))
		headers = append(headers, size[:]...)
		headers = append(headers, pair[1]...)
	}
	return kiroTestFrame(headers, []byte(payload))
}
func kiroOfflineEvent(name, payload string) []byte { return kiroOfflineFrame("event", name, payload) }
func kiroOfflineTestLimits() kiroOfflineLimits {
	return kiroOfflineLimits{MessageBytes:4096, TotalPayloadBytes:16384, TextBytes:4096, Events:20, Tools:4, ArgumentBytes:1024}
}
func kiroOfflineJoin(frames ...[]byte) []byte { return bytes.Join(frames, nil) }

func TestKiroOfflineResponseConnectedTextReasoningUsage(t *testing.T) {
	wire := kiroOfflineJoin(
		kiroOfflineEvent("reasoningContentEvent", `{"text":"synthetic thought","signature":"opaque-fixture"}`),
		kiroOfflineEvent("assistantResponseEvent", `{"content":"hello 中"}`),
		kiroOfflineEvent("metadataEvent", `{"tokenUsage":{"outputTokens":2,"uncachedInputTokens":9007199254740993},"stopReason":"END_TURN"}`),
	)
	r, err := collectKiroOfflineResponse(context.Background(), kiroFragmentReader{bytes.NewReader(wire)}, kiroOfflineTestLimits())
	if err != nil || r.StopReason != StopReasonStop || len(r.Parts) != 2 || r.Parts[0].Kind != "reasoning" || r.Parts[0].Text != "synthetic thought" || r.Parts[0].Signature != "opaque-fixture" || r.Parts[1].Text != "hello 中" || r.Usage.TokenCounts["uncachedInputTokens"] != 9007199254740993 {
		t.Fatalf("offline response: %#v, %v", r, err)
	}
}

func kiroOfflineToolFrames() [][]byte {
	return [][]byte{
		kiroOfflineEvent("toolUseEvent", `{"name":"lookup","toolUseId":"fixture-1","input":{}}`),
		kiroOfflineEvent("toolUseEvent", `{"input":"{\"q\":\"demo\"}"}`),
		kiroOfflineEvent("toolUseEvent", `{"stop":true}`),
	}
}

func TestKiroOfflineResponseStagesToolsUntilValidEnd(t *testing.T) {
	frames := kiroOfflineToolFrames()
	frames = append(frames, kiroOfflineEvent("metadataEvent", `{"stopReason":"TOOL_USE"}`))
	r, err := collectKiroOfflineResponse(context.Background(), bytes.NewReader(kiroOfflineJoin(frames...)), kiroOfflineTestLimits())
	if err != nil || r.StopReason != StopReasonToolUse || len(r.Parts) != 1 || r.Parts[0].Tool == nil || string(r.Parts[0].Tool.Arguments) != `{"q":"demo"}` {
		t.Fatalf("tool staging: %#v, %v", r, err)
	}
	frames[len(frames)-1] = kiroOfflineEvent("metadataEvent", `{"stopReason":"MAX_TOKENS"}`)
	r, err = collectKiroOfflineResponse(context.Background(), bytes.NewReader(kiroOfflineJoin(frames...)), kiroOfflineTestLimits())
	if err != nil || r.StopReason != StopReasonLength || len(r.Parts) != 0 {
		t.Fatalf("length termination released tools: %#v, %v", r, err)
	}
}

func TestKiroOfflineResponseFailureNeverReturnsPartialData(t *testing.T) {
	text := kiroOfflineEvent("assistantResponseEvent", `{"content":"partial"}`)
	end := kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`)
	badCRC := append([]byte(nil), end...)
	badCRC[len(badCRC)-1] ^= 1
	tools := kiroOfflineToolFrames()
	for _, wire := range [][]byte{
		text, // EOF alone is not completion evidence.
		kiroOfflineJoin(text, badCRC), kiroOfflineJoin(text, end[:len(end)-1]),
		kiroOfflineJoin(text, kiroOfflineEvent("metadataEvent", `{"stopReason":"CONTENT_FILTERED"}`)),
		kiroOfflineJoin(text, end, kiroOfflineEvent("metadataEvent", `{"stopReason":"TOOL_USE"}`)),
		kiroOfflineJoin(end, text),
		kiroOfflineJoin(tools[0], tools[1], kiroOfflineEvent("metadataEvent", `{"stopReason":"TOOL_USE"}`)),
		kiroOfflineJoin(tools[0], text, tools[1], tools[2], kiroOfflineEvent("metadataEvent", `{"stopReason":"TOOL_USE"}`)),
		kiroOfflineJoin(text, kiroOfflineEvent("meteringEvent", `{"usage":0.25}`), end),
		kiroOfflineJoin(text, kiroOfflineEvent("assistantResponseEvent", `{"content":null}`), end),
		kiroOfflineJoin(text, kiroOfflineEvent("assistantResponseEvent", `{"content":"a","content":"b"}`), end),
		kiroOfflineJoin(tools[0], tools[1], tools[2], kiroOfflineEvent("metadataEvent", `{"stopReason":"CONTENT_FILTERED"}`)),
	} {
		r, err := collectKiroOfflineResponse(context.Background(), bytes.NewReader(wire), kiroOfflineTestLimits())
		if err == nil || !reflect.DeepEqual(r, kiroOfflineResponse{}) {
			t.Fatalf("failure exposed partial data: %#v, %v", r, err)
		}
	}
}

func TestKiroOfflineAllowsMetadataOnlyAfterStop(t *testing.T) {
	wire := kiroOfflineJoin(kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`), kiroOfflineEvent("metadataEvent", `{"tokenUsage":{"outputTokens":0}}`))
	r, err := collectKiroOfflineResponse(context.Background(), bytes.NewReader(wire), kiroOfflineTestLimits())
	if err != nil || r.StopReason != StopReasonStop || r.Usage.TokenCounts["outputTokens"] != 0 {
		t.Fatalf("split terminal metadata rejected: %#v, %v", r, err)
	}
}

func TestKiroOfflineExceptionPreservesSafeNameOnly(t *testing.T) {
	wire := kiroOfflineFrame("exception", "ThrottlingException", "private-fixture-body-not-json")
	r, err := collectKiroOfflineResponse(context.Background(), bytes.NewReader(wire), kiroOfflineTestLimits())
	var exception *kiroOfflineException
	if !errors.As(err, &exception) || exception.Name != "ThrottlingException" || !errors.Is(err, errKiroEnvelopeException) || !reflect.DeepEqual(r, kiroOfflineResponse{}) || bytes.Contains([]byte(err.Error()), []byte("private-fixture")) {
		t.Fatalf("unsafe exception: %#v, %v", r, err)
	}
}

type kiroOfflineCancelReader struct { reader io.Reader; cancel context.CancelFunc; closed bool }
func (r *kiroOfflineCancelReader) Read(p []byte) (int, error) { r.cancel(); return r.reader.Read(p) }
func (r *kiroOfflineCancelReader) Close() error { r.closed = true; return nil }

func TestKiroOfflineCancellationAndReaderOwnership(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &kiroOfflineCancelReader{reader: bytes.NewReader(kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`)), cancel:cancel}
	r, err := collectKiroOfflineResponse(ctx, reader, kiroOfflineTestLimits())
	if !errors.Is(err, errKiroOfflineCancelled) || !reflect.DeepEqual(r, kiroOfflineResponse{}) || reader.closed {
		t.Fatalf("cancel/ownership violation: %#v, %v", r, err)
	}
}

func TestKiroOfflineAggregateBudgets(t *testing.T) {
	wire := kiroOfflineJoin(kiroOfflineEvent("assistantResponseEvent", `{"content":"1234"}`), kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`))
	for _, change := range []func(*kiroOfflineLimits){
		func(l *kiroOfflineLimits) { l.MessageBytes = 16 },
		func(l *kiroOfflineLimits) { l.TotalPayloadBytes = 2 },
		func(l *kiroOfflineLimits) { l.TextBytes = 3 },
		func(l *kiroOfflineLimits) { l.Events = 1 },
	} {
		limits := kiroOfflineTestLimits(); change(&limits)
		r, err := collectKiroOfflineResponse(context.Background(), bytes.NewReader(wire), limits)
		if err == nil || !reflect.DeepEqual(r, kiroOfflineResponse{}) { t.Fatal("budget bypass or partial return") }
	}
}

func TestKiroOfflineToolBudgets(t *testing.T) {
	wire := kiroOfflineJoin(
		kiroOfflineEvent("toolUseEvent", `{"name":"lookup","toolUseId":"1","input":{"q":"demo"},"stop":true}`),
		kiroOfflineEvent("toolUseEvent", `{"name":"lookup","toolUseId":"2","input":{},"stop":true}`),
		kiroOfflineEvent("metadataEvent", `{"stopReason":"TOOL_USE"}`),
	)
	for _, change := range []func(*kiroOfflineLimits){
		func(l *kiroOfflineLimits) { l.Tools = 1 },
		func(l *kiroOfflineLimits) { l.ArgumentBytes = 2 },
	} {
		limits := kiroOfflineTestLimits(); change(&limits)
		r, err := collectKiroOfflineResponse(context.Background(), bytes.NewReader(wire), limits)
		if err == nil || !reflect.DeepEqual(r, kiroOfflineResponse{}) { t.Fatal("tool budget released partial calls") }
	}
}

type kiroOfflineFailReader struct{}
func (kiroOfflineFailReader) Read([]byte) (int, error) { return 0, errors.New("private-reader-error-fixture") }

func TestKiroOfflineReaderErrorsAreRedacted(t *testing.T) {
	reader := io.MultiReader(bytes.NewReader(kiroOfflineEvent("assistantResponseEvent", `{"content":"partial"}`)), kiroOfflineFailReader{})
	r, err := collectKiroOfflineResponse(context.Background(), reader, kiroOfflineTestLimits())
	if err == nil || bytes.Contains([]byte(err.Error()), []byte("private-reader")) || !reflect.DeepEqual(r, kiroOfflineResponse{}) {
		t.Fatalf("reader error leaked partial/body data: %#v, %v", r, err)
	}
}

type kiroOfflineWrappedEOFReader struct { partial []byte }
func (r kiroOfflineWrappedEOFReader) Read(p []byte) (int, error) {
	return copy(p, r.partial), errors.Join(io.EOF, errors.New("partial-prelude-fixture"))
}

func TestKiroOfflineWrappedEOFNeverAuthorizesCompletion(t *testing.T) {
	valid := kiroOfflineJoin(kiroOfflineEvent("assistantResponseEvent", `{"content":"staged"}`), kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`))
	for _, partial := range [][]byte{nil, {0, 0, 0}} {
		reader := io.MultiReader(bytes.NewReader(valid), kiroOfflineWrappedEOFReader{partial:partial})
		r, err := collectKiroOfflineResponse(context.Background(), reader, kiroOfflineTestLimits())
		if err == nil || !reflect.DeepEqual(r, kiroOfflineResponse{}) {
			t.Fatalf("wrapped EOF released staged result: %#v, %v", r, err)
		}
	}
}
