package pigo

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"testing"
)

func TestKiroHostContextAuthoringAndInitialRecord(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer"}}}`)
	params := map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer"}}}
	tool := Tool{Name: "lookup", Parameters: params}
	a, b := "A", "B"
	for _, pointer := range []bool{false, true} {
		system := SystemMessage{Content: "base", Sections: map[string]*string{"b": &b, "a": &a}, ToolsAdded: []Tool{tool}}
		var initial Message = system
		if pointer {
			initial = &system
		}
		input := Context{Messages: []Message{initial, UserMessage{Content: "question"}}}
		out, selected, err := prepareKiroHostContext(input, map[string]json.RawMessage{"lookup": schema}, 4096)
		if err != nil || out.SystemPrompt != "base\n\nA\n\nB" || len(out.Tools) != 1 || len(out.Messages) != 1 {
			t.Fatal("initial projection", err)
		}
		if len(input.Messages) != 2 || input.SystemPrompt != "" {
			t.Fatal("mutated input")
		}
		selected["lookup"][0] = '!'
		if schema[0] != '{' {
			t.Fatal("raw aliases catalog")
		}
	}
	input := Context{SystemPrompt: "base", Tools: []Tool{tool}, Messages: []Message{UserMessage{Content: "question"}}}
	out, _, err := prepareKiroHostContext(input, map[string]json.RawMessage{"lookup": schema}, 4096)
	if err != nil || !reflect.DeepEqual(input, out) {
		t.Fatal("authoring changed", err)
	}
	// This does not silently lower system instructions: policy remains mandatory.
	if prepared, _, err := prepareKiroSystemPrompt(out, "", 4096); err == nil || len(prepared.Messages) != 0 {
		t.Fatal("system policy bypass")
	}
}

func TestKiroHostContextRejectsAmbiguousAndDynamicSystem(t *testing.T) {
	user := UserMessage{Content: "question"}
	var nilSystem *SystemMessage
	cases := []Context{
		{Messages: []Message{user, SystemMessage{Content: "later"}}},
		{Messages: []Message{SystemMessage{}, SystemMessage{}, user}},
		{SystemPrompt: "authoring", Messages: []Message{SystemMessage{}, user}},
		{Tools: []Tool{{Name: "x"}}, Messages: []Message{SystemMessage{}, user}},
		{Messages: []Message{nilSystem, user}},
		{Messages: []Message{SystemMessage{ToolsRemoved: []ToolReference{{Name: "x"}}}, user}},
		{Messages: []Message{SystemMessage{Sections: map[string]*string{"remove": nil}}, user}},
		{HostedTools: []HostedTool{{Name: "hosted"}}, Messages: []Message{user}},
	}
	for i, input := range cases {
		out, schemas, err := prepareKiroHostContext(input, nil, 4096)
		if err != errKiroHostContext || !reflect.DeepEqual(out, Context{}) || schemas != nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

func TestKiroHostContextSchemaEvidenceNeverMarshaled(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","limit":9007199254740993}`)
	good := map[string]any{"type": "object", "limit": json.Number("9007199254740993")}
	input := Context{Tools: []Tool{{Name: "lookup", Parameters: good}}, Messages: []Message{UserMessage{Content: "question"}}}
	catalog := map[string]json.RawMessage{"lookup": raw, "unused": []byte("not selected")}
	if _, _, err := prepareKiroHostContext(input, catalog, 4096); err != nil {
		t.Fatal(err)
	}
	for _, parameters := range []any{nil, map[string]any(nil), kiroProjectionMustNotMarshal{}, map[string]any{"type": "object", "limit": float64(9007199254740993)}, map[string]any{"type": "object"}} {
		input.Tools[0].Parameters = parameters
		if _, _, err := prepareKiroHostContext(input, catalog, 4096); err == nil {
			t.Fatal("mismatch accepted")
		}
	}
	input.Tools[0].Parameters = good
	for _, invalid := range []json.RawMessage{[]byte(`{"type":"object","type":"object"}`), []byte(`[]`), nil} {
		if _, _, err := prepareKiroHostContext(input, map[string]json.RawMessage{"lookup": invalid}, 4096); err == nil {
			t.Fatal("invalid raw")
		}
	}
	if _, _, err := prepareKiroHostContext(input, nil, 4096); err == nil {
		t.Fatal("missing catalog")
	}
	input.Tools = append(input.Tools, input.Tools[0])
	if _, _, err := prepareKiroHostContext(input, catalog, 4096); err == nil {
		t.Fatal("duplicate tool")
	}
}

func TestKiroHostContextBudgetsAndBoundedEvidence(t *testing.T) {
	input := Context{Messages: []Message{SystemMessage{Content: "12345"}, UserMessage{Content: "q"}}}
	if _, _, err := prepareKiroHostContext(input, nil, 4); err == nil {
		t.Fatal("system budget")
	}
	input = Context{Messages: make([]Message, 4097)}
	if _, _, err := prepareKiroHostContext(input, nil, 4096); err == nil {
		t.Fatal("message budget")
	}
	circular := map[string]any{}
	circular["self"] = circular
	input = Context{Tools: []Tool{{Name: "x", Parameters: circular}}}
	if _, _, err := prepareKiroHostContext(input, map[string]json.RawMessage{"x": []byte(`{"self":{}}`)}, 4096); err == nil {
		t.Fatal("cyclic evidence")
	}
	input.Tools[0].Parameters = map[string]any{}
	if _, _, err := prepareKiroHostContext(input, map[string]json.RawMessage{"x": []byte(`{}`)}, 1); err == nil {
		t.Fatal("invalid budget")
	}
}

func TestKiroHostContextSystemOnlyRequestAndSeparatorBudget(t *testing.T) {
	value := "b"
	input := Context{Messages: []Message{SystemMessage{Content: "a", Sections: map[string]*string{"s": &value}}, UserMessage{Content: "question"}}}
	// Rendered text is four bytes. The conservative input budget also counts
	// the section key, so five is the first accepted input budget.
	if _, _, err := prepareKiroHostContext(input, nil, 4); err == nil {
		t.Fatal("section key budget omitted")
	}
	out, schemas, err := prepareKiroHostContext(input, nil, 5)
	if err != nil || out.SystemPrompt != "a\n\nb" || len(out.SystemPrompt) > 5 || len(out.Tools) != 0 || schemas != nil {
		t.Fatal("rendered size", err)
	}
	options := kiroHTTPRequestFixture()
	options.SystemPromptPolicy = kiroSystemPromptPrependFirstUser
	request, err := newKiroOfflineHTTPRequest(context.Background(), out, schemas, options)
	if err != nil {
		t.Fatal("system-only request", err)
	}
	defer request.Body.Close()
	body, _ := io.ReadAll(request.Body)
	if !bytes.Contains(body, []byte(`a\n\nb\n\nquestion`)) {
		t.Fatal("system lowering missing")
	}
}

func TestKiroHostContextNativeStringArraySchemaEvidence(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","required":["query"]}`)
	for _, required := range []any{[]string{"query"}, []any{"query"}} {
		input := Context{Tools: []Tool{{Name: "search", Parameters: map[string]any{"type": "object", "required": required}}}}
		if _, selected, err := prepareKiroHostContext(input, map[string]json.RawMessage{"search": raw}, 4096); err != nil || string(selected["search"]) != string(raw) {
			t.Fatal("native string array", err)
		}
	}
	for _, required := range []any{[]string(nil), []string{}, []string{"different"}, []int{1}} {
		if kiroToolArgumentEvidenceMatches(`{"required":["query"]}`, map[string]any{"required": required}, 4096) {
			t.Fatal("mismatch accepted")
		}
	}
	if kiroToolArgumentEvidenceMatches(`{"required":[1]}`, map[string]any{"required": []string{"1"}}, 4096) {
		t.Fatal("coerced number")
	}
}
