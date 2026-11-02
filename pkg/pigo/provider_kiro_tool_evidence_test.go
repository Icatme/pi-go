package pigo

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestKiroToolEvidenceMatchingProviderRepresentations(t *testing.T) {
	for _, raw := range []string{`{}`, `{"s":"示例","b":true,"n":null,"list":[1,0.1,{"a":false}]}`, `{"n":1e3,"z":-0}`, `{"n":5e-324}`} {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			t.Fatal(err)
		}
		if !kiroToolArgumentEvidenceMatches(raw, parsed, 4096) {
			t.Fatal("ordinary provider pair rejected", raw)
		}
	}
	for _, parsed := range []map[string]any{{"n": json.Number("9007199254740993")}, {"n": int64(9007199254740993)}, {"n": uint64(9007199254740993)}} {
		if !kiroToolArgumentEvidenceMatches(`{"n":9007199254740993}`, parsed, 4096) {
			t.Fatal("exact integer pair rejected")
		}
	}
	if !kiroToolArgumentEvidenceMatches(`{"n":1.00}`, map[string]any{"n": json.Number("1e0")}, 4096) {
		t.Fatal("equivalent exact decimals rejected")
	}
}

func TestKiroToolEvidenceRejectsPrecisionLossEditsAndCustomValues(t *testing.T) {
	for _, tc := range []struct {
		raw    string
		parsed map[string]any
	}{
		{`{"n":9007199254740993}`, map[string]any{"n": float64(9007199254740992)}},
		{`{"n":0.10000000000000001}`, map[string]any{"n": 0.1}},
		{`{"n":1e-400}`, map[string]any{"n": float64(0)}},
		{`{"n":1}`, map[string]any{"n": math.Inf(1)}},
		{`{"n":1}`, map[string]any{"n": math.NaN()}},
		{`{"n":1}`, map[string]any{"n": "1"}},
		{`{"n":1}`, map[string]any{"n": json.Number("1/1")}},
		{`{"n":1}`, map[string]any{"n": json.Number("true")}},
		{`{"n":1}`, map[string]any{"n": json.Number(" 1")}},
		{`{"n":1}`, map[string]any{"n": kiroProjectionMustNotMarshal{}}},
		{`{"a":[]}`, map[string]any{"a": []any(nil)}},
		{`{"a":{}}`, map[string]any{"a": map[string]any(nil)}},
		{`{"a":true}`, map[string]any{"a": false}},
		{`{"a":1}`, map[string]any{}},
		{`{"a":1}`, map[string]any{"a": 1, "b": 2}},
		{`{"n":1,"n":2}`, map[string]any{"n": 2}},
		{`{"n":1e1025}`, map[string]any{"n": json.Number("1e1025")}},
		{`{"n":` + strings.Repeat("1", 129) + `}`, map[string]any{"n": json.Number(strings.Repeat("1", 129))}},
	} {
		if kiroToolArgumentEvidenceMatches(tc.raw, tc.parsed, 4096) {
			t.Fatal("unsafe representation accepted", tc.raw)
		}
	}
	cyclic := map[string]any{}
	cyclic["a"] = cyclic
	if kiroToolArgumentEvidenceMatches(`{"a":{}}`, cyclic, 4096) {
		t.Fatal("cyclic input accepted")
	}
	if kiroToolArgumentEvidenceMatches(`{"a":1}`, map[string]any{"a": 1}, 2) {
		t.Fatal("raw budget ignored")
	}
}

func TestKiroToolEvidenceReplayPreservesRawAndDoesNotMutate(t *testing.T) {
	ctx, schemas := kiroToolProjectionFixture()
	assistant := ctx.Messages[1].(AssistantMessage)
	call := assistant.Content[1].(ToolCall)
	call.RawArguments = ` {"id":1.00,"list":[true,"示例"]} `
	if err := json.Unmarshal([]byte(call.RawArguments), &call.Arguments); err != nil {
		t.Fatal(err)
	}
	before := cloneMap(call.Arguments)
	assistant.Content[1] = call
	ctx.Messages[1] = assistant
	got, err := projectKiroToolConversation(ctx, "fixture-model", schemas, 10, 10, 4096)
	if err != nil || string(got.History[1].Assistant.Calls[0].Input) != call.RawArguments || !reflect.DeepEqual(call.Arguments, before) {
		t.Fatal("dual representation replay changed evidence", err)
	}
	call.Arguments["id"] = float64(2)
	if _, err := projectKiroToolConversation(ctx, "fixture-model", schemas, 10, 10, 4096); err == nil {
		t.Fatal("edited parsed data replayed old raw")
	}
	// Normal persistence loses raw. Matching parsed-only input remains unsupported.
	call.RawArguments = ""
	assistant.Content[1] = call
	ctx.Messages[1] = assistant
	if _, err := projectKiroToolConversation(ctx, "fixture-model", schemas, 10, 10, 4096); err == nil {
		t.Fatal("missing raw was reconstructed")
	}
}

func TestKiroToolEvidenceTraversalBudget(t *testing.T) {
	raw := `{"a":[` + strings.Repeat("0,", 4096) + `0]}`
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatal(err)
	}
	if kiroToolArgumentEvidenceMatches(raw, parsed, len(raw)) {
		t.Fatal("node budget ignored")
	}
	// Existing raw-only path is not retroactively constrained by comparison work.
	if !kiroToolArgumentEvidenceMatches(raw, nil, len(raw)) {
		t.Fatal("raw-only contract changed")
	}
}

func TestKiroToolEvidenceIntegerFloatChecksActualBinaryValue(t *testing.T) {
	for _, raw := range []string{`{"n":1000000000000000100}`, `{"n":-1000000000000000100}`, `{"n":1.0000000000000001e18}`, `{"n":1000000000000000100.0}`} {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			t.Fatal(err)
		}
		if kiroToolArgumentEvidenceMatches(raw, parsed, 4096) {
			t.Fatal("shortest decimal concealed integer rounding", raw)
		}
	}
	for _, raw := range []string{`{"n":1000000000000000128}`, `{"n":-1000000000000000128}`, `{"n":1.000000000000000128e18}`} {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			t.Fatal(err)
		}
		if !kiroToolArgumentEvidenceMatches(raw, parsed, 4096) {
			t.Fatal("exact binary integer rejected", raw)
		}
	}
}
