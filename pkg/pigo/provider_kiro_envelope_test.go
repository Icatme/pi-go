package pigo

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func kiroTestEnvelopeMessage(kind, name string, payload []byte) kiroEventStreamMessage {
	key := ":event-type"
	if kind == "exception" {
		key = ":exception-type"
	}
	return kiroEventStreamMessage{
		Headers: map[string]kiroEventStreamHeader{
			":message-type": {Type: 7, Value: kind},
			":content-type": {Type: 7, Value: "application/json"},
			key:             {Type: 7, Value: name},
		},
		Payload: payload,
	}
}

func TestKiroEnvelopeSyntheticCorpus(t *testing.T) {
	data, err := os.ReadFile("testdata/kiro/synthetic-wire.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Cases []struct {
			Name   string `json:"name"`
			Frames []struct {
				Wire string `json:"wire_hex"`
			} `json:"frames"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatal(err)
	}
	if len(corpus.Cases) != 12 {
		t.Fatalf("unexpected corpus size: %d", len(corpus.Cases))
	}
	for _, tc := range corpus.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			for _, frame := range tc.Frames {
				wire, err := hex.DecodeString(frame.Wire)
				if err != nil {
					t.Fatal(err)
				}
				d, err := newKiroEventStreamDecoder(bytes.NewReader(wire), len(wire))
				if err != nil {
					t.Fatal(err)
				}
				message, err := d.next()
				if err != nil {
					t.Fatal(err)
				}
				_, err = classifyKiroEnvelope(message)
				wantError := tc.Name == "unknown_tool_shaped_event" || tc.Name == "invalid_payload_json" || message.Headers[":message-type"].Value == "exception"
				if (err != nil) != wantError {
					t.Fatalf("classification error = %v, wantError = %v", err, wantError)
				}
				// Other fixture expectations concern future event semantics,
				// NOT assertions that tools, usage, or lifecycle are valid.
			}
		})
	}
}

func TestKiroEnvelopeRejectsMalformed(t *testing.T) {
	for _, payload := range [][]byte{nil, []byte("null"), []byte("[]"), []byte("{} {}"), []byte("\u00a0{}\u00a0"), []byte("\v{}\v"), []byte("{\"x\":NaN}"), {'{', '"', 'x', '"', ':', '"', 255, '"', '}'}} {
		if _, err := classifyKiroEnvelope(kiroTestEnvelopeMessage("event", "assistantResponseEvent", payload)); !errors.Is(err, errKiroEnvelope) {
			t.Errorf("accepted invalid JSON object: %x", payload)
		}
	}
	for _, mutate := range []func(*kiroEventStreamMessage){
		func(m *kiroEventStreamMessage) { delete(m.Headers, ":message-type") },
		func(m *kiroEventStreamMessage) { delete(m.Headers, ":content-type") },
		func(m *kiroEventStreamMessage) {
			m.Headers[":message-type"] = kiroEventStreamHeader{Type: 0, Value: true}
		},
		func(m *kiroEventStreamMessage) {
			m.Headers[":content-type"] = kiroEventStreamHeader{Type: 7, Value: "text/plain"}
		},
		func(m *kiroEventStreamMessage) {
			m.Headers[":exception-type"] = kiroEventStreamHeader{Type: 7, Value: "error"}
		},
		func(m *kiroEventStreamMessage) {
			m.Headers[":event-type"] = kiroEventStreamHeader{Type: 7, Value: "futureEvent"}
		},
	} {
		m := kiroTestEnvelopeMessage("event", "assistantResponseEvent", []byte("{}"))
		mutate(&m)
		if _, err := classifyKiroEnvelope(m); !errors.Is(err, errKiroEnvelope) {
			t.Fatal("accepted malformed headers")
		}
	}
}

func TestKiroEnvelopeExceptionAliasesAreTerminalAndRedacted(t *testing.T) {
	for _, name := range []string{"error", "throttlingError", "validationError", "serviceUnavailableError", "InternalServerException", "ThrottlingException", "ValidationException", "ServiceUnavailableException"} {
		for _, kind := range []string{"event", "exception"} {
			payload := []byte(` {"message":"fixture"} `)
			e, err := classifyKiroEnvelope(kiroTestEnvelopeMessage(kind, name, payload))
			if !errors.Is(err, errKiroEnvelopeException) || e.Kind != "exception" || e.Name != name || len(e.Payload) != 0 || !e.PayloadValid {
				t.Fatalf("%s/%s: %#v, %v", kind, name, e, err)
			}
		}
	}
	for _, kind := range []string{"error", "unknown"} {
		if _, err := classifyKiroEnvelope(kiroTestEnvelopeMessage(kind, "error", []byte("{}"))); !errors.Is(err, errKiroEnvelope) {
			t.Fatal("unsupported message kind accepted")
		}
	}
}

func TestKiroEnvelopeHeaderOnlyExceptionPreservesKnownType(t *testing.T) {
	for _, payload := range [][]byte{nil, []byte("not-json-secret-fixture"), []byte("[]"), []byte("{\"message\":\"fixture\"}")} {
		m := kiroTestEnvelopeMessage("exception", "ThrottlingException", payload)
		delete(m.Headers, ":content-type")
		e, err := classifyKiroEnvelope(m)
		if !errors.Is(err, errKiroEnvelopeException) || e.Name != "ThrottlingException" || e.Kind != "exception" || len(e.Payload) != 0 {
			t.Fatalf("lost header identity or retained body: %#v, %v", e, err)
		}
		wantValid := bytes.HasPrefix(payload, []byte("{"))
		if e.PayloadValid != wantValid {
			t.Fatalf("payload validity = %v, want %v", e.PayloadValid, wantValid)
		}
	}
}
