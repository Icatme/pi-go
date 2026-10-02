package jsontext

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidateUnicodeEscapeBoundaries(t *testing.T) {
	for _, raw := range []string{
		`"\ud800\udc00"`, `"\udbff\udfff"`, `"\ud83d\ude00\ud83d\ude00"`,
		`"\ud7ff\ue000"`, `"\\ud800"`, `"\\\\udc00"`, `"\"\\udfff"`,
		`{"escaped\"key":"\ud83d\ude00"}`, `0`, `null`,
	} {
		if err := ValidateUnicode([]byte(raw)); err != nil {
			t.Errorf("valid JSON string rejected: %q err=%v", raw, err)
		}
	}
	for _, raw := range []string{`"\ud800"`, `"\udbff\ue000"`, `"\ud800\udbff"`, `"\udfff"`, `{"\ud800":true}`} {
		if err := ValidateUnicode([]byte(raw)); !errors.Is(err, ErrInvalidUnicode) {
			t.Errorf("unpaired surrogate accepted: %q err=%v", raw, err)
		}
	}
	for _, raw := range []string{`"\u123"`, `"\ud800`, `"\ud83d\uZZZZ"`, `{"key"}`, `"quote`, `{} []`} {
		if err := ValidateUnicode([]byte(raw)); !errors.Is(err, ErrInvalidJSON) {
			t.Errorf("malformed JSON accepted: %q err=%v", raw, err)
		}
	}
}

func TestValidateStringsBeforeGoUTF8Replacement(t *testing.T) {
	invalid := string([]byte{0xff})
	type text struct{ ID string }
	for _, value := range []any{
		invalid, map[string]any{invalid: "id"}, map[string]any{"id": invalid},
		[]any{map[string]string{"id": invalid}}, []string{invalid}, &text{ID: invalid},
		json.RawMessage(`{"id":"\ud800"}`), json.RawMessage("\"" + invalid + "\""),
	} {
		if err := ValidateStrings(value); !errors.Is(err, ErrInvalidUnicode) {
			t.Errorf("Go strings would be repaired: %T err=%v", value, err)
		}
	}
	for _, value := range []any{nil, json.RawMessage(nil), []byte{0xff}, "😀�", map[string]string{"😀": "�"}, []text{{ID: "😀�"}}} {
		if err := ValidateStrings(value); err != nil {
			t.Errorf("valid Go JSON strings rejected: %T err=%v", value, err)
		}
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	if err := ValidateStrings(cycle); !errors.Is(err, ErrValueLimit) {
		t.Fatalf("cyclic Go value validation was unbounded: %v", err)
	}
	var pointerCycle any
	pointerCycle = &pointerCycle
	if err := ValidateStrings(pointerCycle); !errors.Is(err, ErrValueLimit) {
		t.Fatalf("cyclic Go pointer validation was unbounded: %v", err)
	}
	var nested any = "😀�"
	for range 100 {
		nested = map[string]any{"next": nested}
	}
	if err := ValidateStrings(nested); err != nil {
		t.Fatalf("Go interface wrappers changed the JSON nesting bound: %v", err)
	}
}
