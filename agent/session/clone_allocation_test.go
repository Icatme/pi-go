package session

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func BenchmarkCloneValidatedLargePayload(b *testing.B) {
	input := struct{ Content string }{Content: strings.Repeat("x", 1<<20)}
	b.ReportAllocs()
	b.SetBytes(int64(len(input.Content)))
	for b.Loop() {
		output, err := cloneValidated(input)
		if err != nil || output.Content != input.Content {
			b.Fatal("clone mismatch", err)
		}
	}
}

func TestCloneValidatedPreservesNumberAndIsolation(t *testing.T) {
	input := map[string]any{"nested": map[string]any{"number": json.Number("9007199254740993"), "list": []any{"original"}}}
	output, err := cloneValidated(input)
	if err != nil {
		t.Fatal(err)
	}
	nested := output["nested"].(map[string]any)
	if got := nested["number"].(json.Number).String(); got != "9007199254740993" {
		t.Fatal(got)
	}
	nested["list"].([]any)[0] = "changed"
	if input["nested"].(map[string]any)["list"].([]any)[0] != "original" {
		t.Fatal("clone aliases input")
	}
	if _, err := cloneValidated(map[string]any{"invalid": math.NaN()}); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}
