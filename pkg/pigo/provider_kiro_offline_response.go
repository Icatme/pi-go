package pigo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
)

var errKiroOfflineResponse = errors.New("kiro: unsupported or incomplete offline response")
var errKiroOfflineCancelled = errors.New("kiro: offline response cancelled")

type kiroOfflineException struct { Name string }
func (e *kiroOfflineException) Error() string { return "kiro: service exception" }
func (e *kiroOfflineException) Unwrap() error { return errKiroEnvelopeException }

type kiroOfflineLimits struct {
	MessageBytes int
	TotalPayloadBytes int
	TextBytes int
	Events int
	Tools int
	ArgumentBytes int
}

// Parts retain text/reasoning/tool ordering. Reasoning fields remain opaque
// event fragments; no signature validation or replay compatibility is implied.
type kiroOfflinePart struct {
	Kind string
	Text string
	Signature string
	Tool *kiroAssembledTool
}
type kiroOfflineResponse struct {
	Parts []kiroOfflinePart
	Usage kiroMetadataSnapshot
	StopReason StopReason
}

// collectKiroOfflineResponse connects parsers using caller-provided fixture or
// cached bytes only. It emits nothing until the WHOLE reader reaches clean EOF
// AND explicit completion evidence passes. Any error returns a zero response.
// Reader ownership stays with the caller: no Close, goroutines or transport.
// Cancellation is checked around each underlying read; interrupting a blocked Read requires
// the caller to cancel/close its reader. Inputs must not mutate concurrently.
func collectKiroOfflineResponse(ctx context.Context, reader io.Reader, limits kiroOfflineLimits) (kiroOfflineResponse, error) {
	zero := kiroOfflineResponse{}
	if ctx == nil || reader == nil || limits.TotalPayloadBytes < 2 || limits.TextBytes < 1 || limits.Events < 1 || limits.Tools < 1 || limits.ArgumentBytes < 2 {
		return zero, errKiroOfflineResponse
	}
	guardedReader := &kiroOfflineContextReader{ctx:ctx, reader:reader}
	decoder, err := newKiroEventStreamDecoder(guardedReader, limits.MessageBytes)
	if err != nil { return zero, errKiroOfflineResponse }
	assembler, err := newKiroToolAssembler(limits.ArgumentBytes, limits.MessageBytes, limits.Events, limits.Tools)
	if err != nil { return zero, errKiroOfflineResponse }
	metadata, err := newKiroMetadataCollector(limits.MessageBytes, limits.Events)
	if err != nil { return zero, errKiroOfflineResponse }
	payloadLeft, textLeft, eventCount, toolCount := limits.TotalPayloadBytes, limits.TextBytes, 0, 0
	var parts []kiroOfflinePart
	terminalMetadataSeen := false
	consumeText := func(text string) bool {
		if len(text) > textLeft { return false }
		textLeft -= len(text)
		return true
	}
	for {
		if ctx.Err() != nil { return zero, errKiroOfflineCancelled }
		frame, err := decoder.next()
		if ctx.Err() != nil { return zero, errKiroOfflineCancelled }
		if guardedReader.failure != nil { return zero, errKiroOfflineResponse }
		// Only the exact sentinel proves no bytes of another prelude were
		// read. A wrapped/joined EOF can accompany partial data or another
		// failure and must never authorize releasing staged results.
		if err == io.EOF { break }
		if err != nil || eventCount >= limits.Events || len(frame.Payload) > payloadLeft {
			return zero, errKiroOfflineResponse
		}
		eventCount++
		payloadLeft -= len(frame.Payload)
		envelope, err := classifyKiroEnvelope(frame)
		if err != nil {
			if errors.Is(err, errKiroEnvelopeException) {
				return zero, &kiroOfflineException{Name: envelope.Name}
			}
			return zero, errKiroOfflineResponse
		}
		if terminalMetadataSeen && envelope.Name != "metadataEvent" {
			return zero, errKiroOfflineResponse
		}
		switch envelope.Name {
		case "metadataEvent":
			if metadata.accept(envelope.Payload) != nil { return zero, errKiroOfflineResponse }
			terminalMetadataSeen = metadata.snapshot.StopReason != ""
		case "toolUseEvent":
			tool, err := assembler.accept(envelope.Payload)
			if err != nil { return zero, errKiroOfflineResponse }
			if tool != nil {
				toolCount++
				parts = append(parts, kiroOfflinePart{Kind: "tool", Tool: tool})
			}
		case "assistantResponseEvent", "reasoningContentEvent":
			if assembler.active != nil { return zero, errKiroOfflineResponse }
			fields, err := decodeKiroJSONObject(envelope.Payload, limits.MessageBytes)
			if err != nil { return zero, errKiroOfflineResponse }
			part := kiroOfflinePart{Kind: "text"}
			if envelope.Name == "assistantResponseEvent" {
				if len(fields) != 1 { return zero, errKiroOfflineResponse }
				part.Text, err = kiroOfflineString(fields["content"])
				if err != nil || !consumeText(part.Text) { return zero, errKiroOfflineResponse }
			} else {
				part.Kind = "reasoning"
				if len(fields) == 0 { return zero, errKiroOfflineResponse }
				for name, raw := range fields {
					value, err := kiroOfflineString(raw)
					if err != nil || !consumeText(value) { return zero, errKiroOfflineResponse }
					switch name {
					case "text": part.Text = value
					case "signature": part.Signature = value
					default: return zero, errKiroOfflineResponse
					}
				}
			}
			parts = append(parts, part)
		default:
			// Citation/context/metering/redacted/unknown extensions require
			// their own validated schemas. Do not silently drop them here.
			return zero, errKiroOfflineResponse
		}
	}
	if assembler.finish() != nil { return zero, errKiroOfflineResponse }
	usage, err := metadata.result()
	if err != nil { return zero, errKiroOfflineResponse }
	decision, err := decideKiroCompletion(kiroCompletionEvidence{RawStopReason: usage.StopReason, StreamEnded: true, CompletedToolCount: toolCount})
	if err != nil { return zero, errKiroOfflineResponse }
	if !decision.ReleaseTools {
		kept := make([]kiroOfflinePart, 0, len(parts))
		for _, part := range parts {
			if part.Kind != "tool" { kept = append(kept, part) }
		}
		parts = kept
	}
	if ctx.Err() != nil { return zero, errKiroOfflineCancelled }
	return kiroOfflineResponse{Parts: parts, Usage: usage, StopReason: decision.Reason}, nil
}

type kiroOfflineContextReader struct { ctx context.Context; reader io.Reader; failure error; emptyReads int }
func (r *kiroOfflineContextReader) Read(p []byte) (int, error) {
	if r.ctx.Err() != nil { return 0, errKiroOfflineCancelled }
	if r.failure != nil { return 0, r.failure }
	if len(p) == 0 { return 0, nil }
 n, err := r.reader.Read(p)
 // A broken reader must not spin forever inside io.ReadFull. This is a
 // consecutive empty-read budget, not a timeout for a blocking Read.
 if n == 0 && err == nil {
  r.emptyReads++
  if r.emptyReads >= 100 { r.failure = io.ErrNoProgress; return 0, r.failure }
 } else { r.emptyReads = 0 }
	if r.ctx.Err() != nil { return n, errKiroOfflineCancelled }
	// io.ReadFull can discard an error accompanying a full requested buffer.
	// Preserve every non-exact-EOF failure independently of that API so a final
	// complete frame plus a transport error can never authorize completion.
	if err != nil && err != io.EOF { r.failure = err }
	return n, err
}

func kiroOfflineString(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' { return "", errKiroOfflineResponse }
	var value string
	if err := json.Unmarshal(raw, &value); err != nil { return "", errKiroOfflineResponse }
	return value, nil
}
