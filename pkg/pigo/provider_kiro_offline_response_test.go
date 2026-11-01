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

type kiroOfflineFullReadErrorReader struct { reader *bytes.Reader; failure error }
func (r *kiroOfflineFullReadErrorReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 && r.reader.Len() == 0 { return n, r.failure }
	return n, err
}

func TestKiroOfflineFullFrameReadErrorCannotBeSwallowed(t *testing.T) {
	frames := append(kiroOfflineToolFrames(), kiroOfflineEvent("metadataEvent", `{"stopReason":"TOOL_USE"}`))
	wire := kiroOfflineJoin(frames...)
	for _, failure := range []error{errors.New("private-transport-fixture"), errors.Join(io.EOF, errors.New("private-wrapped-fixture"))} {
		reader := &kiroOfflineFullReadErrorReader{reader:bytes.NewReader(wire), failure:failure}
		r, err := collectKiroOfflineResponse(context.Background(), reader, kiroOfflineTestLimits())
		if err == nil || !reflect.DeepEqual(r, kiroOfflineResponse{}) || bytes.Contains([]byte(err.Error()), []byte("private-")) {
			t.Fatalf("full-read error released staged tools: %#v, %v", r, err)
		}
	}
}

type kiroOfflineEmptyReader struct { reads int }
func (r *kiroOfflineEmptyReader) Read(p []byte) (int,error) { r.reads++; return 0,nil }
func TestKiroOfflineReaderNoProgressIsTerminal(t *testing.T) {
 r := &kiroOfflineEmptyReader{}
 got, err := collectKiroOfflineResponse(context.Background(), r, kiroOfflineTestLimits())
 if err == nil || !reflect.DeepEqual(got,kiroOfflineResponse{}) || r.reads != 100 { t.Fatalf("unbounded empty reader: %d, %#v, %v",r.reads,got,err) }
 guard := &kiroOfflineContextReader{ctx:context.Background(), reader:r}
 for i:=0; i<100; i++ { guard.Read(make([]byte,1)) }
 before:=r.reads
 if _, err:=guard.Read(make([]byte,1)); !errors.Is(err,io.ErrNoProgress) || r.reads!=before { t.Fatal("no-progress not sticky") }
}

type kiroOfflineIntermittentReader struct { calls int }
func (r *kiroOfflineIntermittentReader) Read(p []byte) (int,error) {
 r.calls++
 if r.calls%100 != 0 { return 0,nil }
 p[0]='x'; return 1,nil
}
func TestKiroOfflinePositiveReadResetsEmptyBudget(t *testing.T) {
 source:=&kiroOfflineIntermittentReader{}
 guard:=&kiroOfflineContextReader{ctx:context.Background(),reader:source}
 for i:=0; i<200; i++ {
  n,err:=guard.Read(make([]byte,1))
  if err!=nil { t.Fatalf("transient empty reads failed at %d: %v",i,err) }
  if (i+1)%100==0 && (n!=1 || guard.emptyReads!=0) { t.Fatal("positive read did not reset budget") }
 }
}

// Preserve event-level provenance; a signature-only fragment is not attached to
// an earlier/later thought by a heuristic and is never made replay-ready.
func TestKiroOfflineReasoningFragmentsRemainOrderedOpaque(t *testing.T) {
 wire:=kiroOfflineJoin(
  kiroOfflineEvent("reasoningContentEvent", `{"text":"first"}`),
  kiroOfflineEvent("reasoningContentEvent", `{"text":"second","signature":"paired"}`),
  kiroOfflineEvent("reasoningContentEvent", `{"signature":"standalone"}`),
  kiroOfflineEvent("assistantResponseEvent", `{"content":"answer"}`),
  kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`),
 )
 got,err:=collectKiroOfflineResponse(context.Background(),bytes.NewReader(wire),kiroOfflineTestLimits())
 want:=[]kiroOfflinePart{
  {Kind:"reasoning",Text:"first"},
  {Kind:"reasoning",Text:"second",Signature:"paired"},
  {Kind:"reasoning",Signature:"standalone"},
  {Kind:"text",Text:"answer"},
 }
 if err!=nil || !reflect.DeepEqual(got.Parts,want) { t.Fatalf("reasoning provenance: %#v, %v",got,err) }
}

func TestKiroOfflineUsageSnapshotsMergeWithoutAccumulation(t *testing.T) {
 wire:=kiroOfflineJoin(
  kiroOfflineEvent("metadataEvent", `{"tokenUsage":{"uncachedInputTokens":3,"cacheReadInputTokens":0}}`),
  kiroOfflineEvent("assistantResponseEvent", `{"content":"answer"}`),
  kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN","tokenUsage":{"uncachedInputTokens":3}}`),
  kiroOfflineEvent("metadataEvent", `{"tokenUsage":{"outputTokens":2,"totalTokens":99}}`),
  kiroOfflineEvent("metadataEvent", `{"tokenUsage":{"outputTokens":2}}`),
 )
 got,err:=collectKiroOfflineResponse(context.Background(),bytes.NewReader(wire),kiroOfflineTestLimits())
 want:=map[string]int64{"uncachedInputTokens":3,"cacheReadInputTokens":0,"outputTokens":2,"totalTokens":99}
 if err!=nil || !reflect.DeepEqual(got.Usage.TokenCounts,want) { t.Fatalf("snapshot merge/sum: %#v, %v",got,err) }
 if _,present:=got.Usage.TokenCounts["cacheWriteInputTokens"];present { t.Fatal("invented missing count") }
}

func TestKiroOfflineTerminalOrderingAndLateFailureRollBack(t *testing.T) {
 for _,late:=range [][]byte{
  kiroOfflineEvent("assistantResponseEvent", `{"content":"late"}`),
  kiroOfflineEvent("reasoningContentEvent", `{"signature":"late"}`),
  kiroOfflineEvent("toolUseEvent", `{"name":"lookup","toolUseId":"next","stop":true}`),
  kiroOfflineEvent("metadataEvent", `{"tokenUsage":{"outputTokens":2}}`),
  kiroOfflineEvent("metadataEvent", `{"tokenUsage":{"unknown":1}}`),
  kiroOfflineEvent("metadataEvent", `{"stopReason":"END_TURN"}`),
 } {
  frames:=kiroOfflineToolFrames()
  frames=append(frames,kiroOfflineEvent("metadataEvent", `{"stopReason":"TOOL_USE","tokenUsage":{"outputTokens":1}}`),late)
  got,err:=collectKiroOfflineResponse(context.Background(),bytes.NewReader(kiroOfflineJoin(frames...)),kiroOfflineTestLimits())
  if err==nil || !reflect.DeepEqual(got,kiroOfflineResponse{}) { t.Fatalf("late contradiction leaked tools: %#v, %v",got,err) }
 }
}
