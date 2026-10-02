package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

func TestValidateJSONRejectsInvalidUnicodeBeforeDecoding(t *testing.T) {
	for _, raw := range []string{
		`"\ud800"`, `"\udfff"`, `"a\ud800b"`, `"\ud800\ud800"`,
		`"\udc00\ud800"`, `"\ud800\u0041"`, `"\ud800\\udc00"`,
		`{"\ud800":"value"}`, `{"id":["\udfff"]}`,
		"{\"id\":\"" + string([]byte{0xff}) + "\"}",
		"{\"" + string([]byte{0xed, 0xa0, 0x80}) + "\":1}",
	} {
		t.Run(raw, func(t *testing.T) {
			err := ValidateJSON(json.RawMessage(raw))
			var invalid *JSONError
			if !errors.As(err, &invalid) || invalid.Code != "invalid_unicode" {
				t.Fatalf("invalid Unicode accepted or misclassified: %q err=%v", raw, err)
			}
		})
	}
	for _, raw := range []string{
		`"\ud83d\ude00"`, `"\uD83D\uDE00"`, `"😀"`, `"�"`, `"\ufffd"`,
		`"\\ud800"`, `"escaped \" quote"`, `{"\ud83d\ude00":"\ufffd"}`,
		`{"😀":["hello",true,null,0.1]}`,
	} {
		if err := ValidateJSON(json.RawMessage(raw)); err != nil {
			t.Errorf("valid Unicode rejected: %s err=%v", raw, err)
		}
	}
}

func TestUnicodeArgumentsAndStoreCannotSilentlyChangeStrings(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	var calls atomic.Int32
	tool := Tool{Name: "write", Invoke: func(_ context.Context, call HostCall) (json.RawMessage, error) {
		calls.Add(1)
		return call.Arguments, nil
	}}
	for _, value := range []string{`{id:"abc\ud800"}`, `{"\udfff":"id"}`, `{nested:["\ud800\ud800"]}`} {
		result, err := s.Run(t.Context(), `try {await tools.write(`+value+`);return "accepted"}catch(e){return e.code+":"+e.reasonCode}`, RunOptions{Tools: []Tool{tool}})
		if err != nil || textOutput(result) != "argument_invalid:invalid_unicode" || len(result.Calls) != 0 || calls.Load() != 0 {
			t.Fatalf("invalid Unicode reached host: %s result=%+v err=%v calls=%d", value, result, err, calls.Load())
		}
	}
	store, err := NewStore(1024)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{`"abc\ud800"`, `{"\udfff":1}`, `["\ud800\ud800"]`} {
		result, err := s.Run(t.Context(), `try {store("bad",`+value+`);return "accepted"}catch(e){return e.code+":"+e.reasonCode}`, RunOptions{Store: store})
		if err != nil || textOutput(result) != "unsupported_json:invalid_unicode" {
			t.Fatalf("invalid Unicode stored: %s result=%+v err=%v", value, result, err)
		}
		result, err = s.Run(t.Context(), `return load("bad")===undefined`, RunOptions{Store: store})
		if err != nil || textOutput(result) != "true" {
			t.Fatalf("rejected store value survived: %+v %v", result, err)
		}
	}
	result, err := s.Run(t.Context(), `store("good",{"\ud83d\ude00":"\ufffd"});return await tools.write(load("good"))`, RunOptions{Store: store, Tools: []Tool{tool}})
	if err != nil || calls.Load() != 1 || textOutput(result) != `{"😀":"�"}` {
		t.Fatalf("valid Unicode changed: %+v err=%v calls=%d", result, err, calls.Load())
	}
}

func TestUnicodeDiscoveryRequestsAndStoreKeysAreRejectedBeforeDecode(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	for _, code := range []string{`await searchTools("abc\ud800")`, `await searchTools("foo",{"\udfff":1})`, `await describeTool("abc\ud800")`, `await describeNamespace("abc\ud800")`} {
		result, err := s.Run(t.Context(), `try {`+code+`;return "accepted"}catch(e){return e.code+":"+e.reasonCode}`, RunOptions{})
		if err != nil || textOutput(result) != "arguments:invalid_unicode" {
			t.Fatalf("Unicode discovery input repaired: %s result=%+v err=%v", code, result, err)
		}
	}
	for _, code := range []string{`store("\ud800",1)`, `store("\ud800",undefined)`, `load("\ud800")`, `text("\ud800")`} {
		result, err := s.Run(t.Context(), `try {`+code+`;return "accepted"}catch(e){return e.code+":"+e.reasonCode}`, RunOptions{})
		if err != nil || textOutput(result) != "unsupported_string:invalid_unicode" {
			t.Fatalf("Unicode raw string repaired: %s result=%+v err=%v", code, result, err)
		}
	}
}

func TestUnicodeHostResultsAndSchemasAreRejected(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	for _, raw := range []json.RawMessage{json.RawMessage(`{"id":"\ud800"}`), json.RawMessage(`{"\udfff":1}`), json.RawMessage("\"" + string([]byte{0xff}) + "\"")} {
		tool := Tool{Name: "read", Invoke: func(context.Context, HostCall) (json.RawMessage, error) { return raw, nil }}
		result, err := s.Run(t.Context(), `try {await tools.read({});return "accepted"}catch(e){return e.code+":"+e.reasonCode}`, RunOptions{Tools: []Tool{tool}})
		if err != nil || textOutput(result) != "result_rejected:invalid_unicode" || len(result.Calls) != 1 {
			t.Fatalf("invalid Unicode result exposed: %q result=%+v err=%v", raw, result, err)
		}
		tool.Parameters = raw
		if _, err := s.Run(t.Context(), `return true`, RunOptions{Tools: []Tool{tool}}); err == nil || !strings.Contains(err.Error(), "invalid_unicode") {
			t.Fatalf("invalid Unicode schema accepted: %q err=%v", raw, err)
		}
	}
}
