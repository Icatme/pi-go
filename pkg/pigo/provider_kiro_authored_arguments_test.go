package pigo

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type kiroAuthoredNoMethods struct{}

func (kiroAuthoredNoMethods) MarshalJSON() ([]byte, error) { panic("caller marshaler executed") }
func (kiroAuthoredNoMethods) String() string               { panic("caller String executed") }

func TestKiroAuthoredArgumentsCurrentValuesAndPrecision(t *testing.T) {
	input := map[string]any{"id": json.Number("9007199254740993"), "signed": int64(-9223372036854775808), "unsigned": uint64(18446744073709551615), "decimal": 0.1, "nested": []any{true, nil, "中", []string{"a"}}, "empty": map[string]any{}}
	got, err := encodeKiroAuthoredToolArguments(input, 4096)
	if err != nil || !bytes.Contains(got, []byte(`"id":9007199254740993`)) || !bytes.Contains(got, []byte(`18446744073709551615`)) {
		t.Fatalf("current values: %s %v", got, err)
	}
	input["id"] = json.Number("9007199254740995")
	changed, err := encodeKiroAuthoredToolArguments(input, 4096)
	if err != nil || !bytes.Contains(changed, []byte(`9007199254740995`)) || !bytes.Contains(got, []byte(`9007199254740993`)) {
		t.Fatal("authored edit or output isolation")
	}
	// A generated object is not claimed to retain original whitespace/key ordering.
	if _, err := encodeKiroAuthoredToolArguments(map[string]any{}, 2); err != nil {
		t.Fatal(err)
	}
}

func TestKiroAuthoredArgumentsRejectAmbiguityAndCustomTypes(t *testing.T) {
	type namedMap map[string]any
	cycle := map[string]any{}
	cycle["self"] = cycle
	for _, input := range []map[string]any{
		nil, {"n": float64(9007199254740992)}, {"n": -float64(9007199254740992)}, {"n": math.Inf(1)}, {"n": math.NaN()},
		{"x": kiroAuthoredNoMethods{}}, {"x": namedMap{"a": 1}}, {"x": (*int)(nil)}, {"x": []any(nil)}, {"x": map[string]any(nil)},
		{"x": string([]byte{0xff})}, {string([]byte{0xff}): "x"}, cycle,
	} {
		if got, err := encodeKiroAuthoredToolArguments(input, 4096); err == nil || got != nil {
			t.Fatal("unsupported authored object accepted")
		}
	}
	for _, literal := range []string{"", "true", "null", "01", "+1", " 1", "1/2", "1e1025", strings.Repeat("1", 129)} {
		if got, err := encodeKiroAuthoredToolArguments(map[string]any{"n": json.Number(literal)}, 4096); err == nil || got != nil {
			t.Fatalf("number accepted: %q", literal)
		}
	}
	for _, number := range []float64{9007199254740991, -9007199254740991, 0.1, math.SmallestNonzeroFloat64} {
		if _, err := encodeKiroAuthoredToolArguments(map[string]any{"n": number}, 4096); err != nil {
			t.Fatal("supported current float rejected", err)
		}
	}
}

func TestKiroAuthoredArgumentsBudgetsAndReadOnlyConcurrency(t *testing.T) {
	input := map[string]any{"escaped": "<>&\n"}
	encoded, err := encodeKiroAuthoredToolArguments(input, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encodeKiroAuthoredToolArguments(input, len(encoded)); err != nil {
		t.Fatal(err)
	}
	if got, err := encodeKiroAuthoredToolArguments(input, len(encoded)-1); err == nil || got != nil {
		t.Fatal("escaped wire budget ignored")
	}
	if _, err := encodeKiroAuthoredToolArguments(map[string]any{"s": strings.Repeat("x", 8192)}, 4096); err == nil {
		t.Fatal("input budget ignored")
	}
	if _, err := encodeKiroAuthoredToolArguments(map[string]any{"a": make([]any, 4096)}, 1<<20); err == nil {
		t.Fatal("node budget ignored")
	}
	var deep any = "leaf"
	for i := 0; i < 66; i++ {
		deep = []any{deep}
	}
	if _, err := encodeKiroAuthoredToolArguments(map[string]any{"deep": deep}, 4096); err == nil {
		t.Fatal("depth budget ignored")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := encodeKiroAuthoredToolArguments(input, 4096)
			if err != nil || !bytes.Equal(got, encoded) {
				t.Error("read-only concurrent encoding changed")
			}
		}()
	}
	wg.Wait()
	if input["escaped"] != "<>&\n" {
		t.Fatal("input mutated")
	}
}

func kiroAuthoredFixture(call ToolCall) (Context, map[string]json.RawMessage) {
	c, s := kiroToolProjectionFixture()
	a := c.Messages[1].(AssistantMessage)
	a.Content[1] = call
	c.Messages[1] = a
	return c, s
}

func TestKiroAuthoredHistoryIsExplicitAndDoesNotForgeRaw(t *testing.T) {
	call := ToolCall{ID: "fixture-1", Name: "lookup", Arguments: map[string]any{"id": json.Number("9007199254740993")}}
	c, s := kiroAuthoredFixture(call)
	if _, err := projectKiroToolConversation(c, "fixture-model", s, 10, 10, 4096); err == nil {
		t.Fatal("strict default accepted missing original evidence")
	}
	got, err := projectKiroToolConversationWithPolicy(c, "fixture-model", s, 10, 10, 4096, kiroToolHistoryAuthored)
	if err != nil || string(got.History[1].Assistant.Calls[0].Input) != `{"id":9007199254740993}` {
		t.Fatalf("authored projection: %#v %v", got, err)
	}
	if c.Messages[1].(AssistantMessage).Content[1].(ToolCall).RawArguments != "" {
		t.Fatal("authored text invented provider evidence")
	}
	call.Arguments["id"] = json.Number("9007199254740995")
	edited, err := projectKiroToolConversationWithPolicy(c, "fixture-model", s, 10, 10, 4096, kiroToolHistoryAuthored)
	if err != nil || string(edited.History[1].Assistant.Calls[0].Input) != `{"id":9007199254740995}` {
		t.Fatal("authored edits ignored")
	}
	if string(got.History[1].Assistant.Calls[0].Input) != `{"id":9007199254740993}` {
		t.Fatal("prior output mutated")
	}
	if _, err := projectKiroToolConversationWithPolicy(c, "fixture-model", s, 10, 10, 4096, 99); err == nil {
		t.Fatal("unknown policy accepted")
	}
}

func TestKiroAuthoredHistoryRestoresCurrentNumbersWithoutOriginalEvidence(t *testing.T) {
	original := ToolCall{ID: "fixture-1", Name: "lookup", RawArguments: ` { "id" : 9007199254740993 } `, Arguments: map[string]any{"id": json.Number("9007199254740993")}}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var restored ToolCall
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&restored); err != nil {
		t.Fatal(err)
	}
	if restored.RawArguments != "" {
		t.Fatal("JSON restored original evidence")
	}
	c, s := kiroAuthoredFixture(restored)
	p, err := projectKiroToolConversationWithPolicy(c, "fixture-model", s, 10, 10, 4096, kiroToolHistoryAuthored)
	if err != nil || string(p.History[1].Assistant.Calls[0].Input) != `{"id":9007199254740993}` {
		t.Fatal("authored restored exact number rejected", err)
	}
	// Ordinary float decoding has already lost precision; do not repair it.
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	c, s = kiroAuthoredFixture(restored)
	if _, err := projectKiroToolConversationWithPolicy(c, "fixture-model", s, 10, 10, 4096, kiroToolHistoryAuthored); err == nil {
		t.Fatal("rounded restored integer accepted")
	}
}

func TestKiroAuthoredHistoryNeverOverridesConflictingOriginalEvidence(t *testing.T) {
	for _, raw := range []string{`{"id":2}`, `{"id":1,"id":1}`, `bad`} {
		c, s := kiroAuthoredFixture(ToolCall{ID: "fixture-1", Name: "lookup", RawArguments: raw, Arguments: map[string]any{"id": 1}})
		got, err := projectKiroToolConversationWithPolicy(c, "fixture-model", s, 10, 10, 4096, kiroToolHistoryAuthored)
		if err == nil || !reflect.DeepEqual(got, kiroToolProjection{}) {
			t.Fatal("conflicting raw silently bypassed")
		}
	}
	c, s := kiroAuthoredFixture(ToolCall{ID: "fixture-1", Name: "lookup", RawArguments: ` { "id" : 1.00 } `, Arguments: map[string]any{"id": 1}})
	p, err := projectKiroToolConversationWithPolicy(c, "fixture-model", s, 10, 10, 4096, kiroToolHistoryAuthored)
	if err != nil || string(p.History[1].Assistant.Calls[0].Input) != `{"id":1}` {
		t.Fatal("authored mode accidentally promised original spelling", err)
	}
	strict, err := projectKiroToolConversation(c, "fixture-model", s, 10, 10, 4096)
	if err != nil || string(strict.History[1].Assistant.Calls[0].Input) != ` { "id" : 1.00 } ` {
		t.Fatal("original mode changed", err)
	}
}

func TestKiroAuthoredArgumentsContainerAndNodeBoundaries(t *testing.T) {
	shared := map[string]any{"value": "same"}
	if _, err := encodeKiroAuthoredToolArguments(map[string]any{"a": shared, "b": shared}, 4096); err != nil {
		t.Fatal("shared acyclic subtree rejected", err)
	}
	// Root object plus array plus 4094 scalars exhausts the 4096-value budget.
	if _, err := encodeKiroAuthoredToolArguments(map[string]any{"items": make([]any, 4094)}, 1<<20); err != nil {
		t.Fatal("exact node budget rejected", err)
	}
	if _, err := encodeKiroAuthoredToolArguments(map[string]any{"items": make([]any, 4095)}, 1<<20); err == nil {
		t.Fatal("node budget +1 accepted")
	}
	for _, levels := range []int{64, 65} {
		var value any = map[string]any{}
		for i := 1; i < levels; i++ {
			value = map[string]any{"child": value}
		}
		_, err := encodeKiroAuthoredToolArguments(value.(map[string]any), 4096)
		if (levels == 64 && err != nil) || (levels == 65 && err == nil) {
			t.Fatalf("container depth %d: %v", levels, err)
		}
	}
}

func TestKiroAuthoredProjectionCountsInputInAggregateBudget(t *testing.T) {
	args := map[string]any{"text": strings.Repeat("x", 256)}
	encoded, err := encodeKiroAuthoredToolArguments(args, 4096)
	if err != nil {
		t.Fatal(err)
	}
	c, s := kiroAuthoredFixture(ToolCall{ID: "fixture-1", Name: "lookup", Arguments: args})
	low, high := 2, 4096
	for low < high {
		mid := (low + high) / 2
		_, err := projectKiroToolConversationWithPolicy(c, "fixture-model", s, 10, 10, mid, kiroToolHistoryAuthored)
		if err != nil {
			low = mid + 1
		} else {
			high = mid
		}
	}
	if low <= len(encoded) {
		t.Fatal("aggregate omitted non-argument content")
	}
	if _, err := projectKiroToolConversationWithPolicy(c, "fixture-model", s, 10, 10, low, kiroToolHistoryAuthored); err != nil {
		t.Fatal("exact aggregate boundary", err)
	}
	got, err := projectKiroToolConversationWithPolicy(c, "fixture-model", s, 10, 10, low-1, kiroToolHistoryAuthored)
	if err == nil || !reflect.DeepEqual(got, kiroToolProjection{}) {
		t.Fatal("aggregate overflow did not return zero")
	}
}
