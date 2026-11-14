package pigo

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Upstream pi #9134: do not flatten the model-facing schema to selected fields.
func TestAnthropicRequestRetainsRootToolConstraints(t *testing.T) {
	const raw = `{"type":"object","properties":{"kind":{"type":"string","enum":["a","b"]},"value":{"type":"integer"}},"required":["kind","value"],"additionalProperties":false,"anyOf":[{"properties":{"kind":{"const":"a"},"value":{"maximum":10}}},{"properties":{"kind":{"const":"b"},"value":{"minimum":20}}}],"allOf":[{"not":{"properties":{"value":{"const":15}}}}]}`
	var schema map[string]any
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(schema)
	for _, oauth := range []bool{false, true} {
		model := Model{ID: "fixture", Provider: "anthropic", API: "anthropic-messages"}
		request := buildAnthropicRequest(model, Context{Tools: []Tool{{Name: "choose", Parameters: schema}}}, AnthropicMessagesProviderOptions{}, oauth, true)
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Tools []struct {
				InputSchema map[string]any `json:"input_schema"`
			} `json:"tools"`
		}
		if err = json.Unmarshal(encoded, &wire); err != nil {
			t.Fatal(err)
		}
		if len(wire.Tools) != 1 || !reflect.DeepEqual(wire.Tools[0].InputSchema, schema) {
			t.Fatalf("root constraints lost, oauth=%v: %s", oauth, encoded)
		}
	}
	after, _ := json.Marshal(schema)
	if string(after) != string(before) {
		t.Fatal("caller schema mutated")
	}
}
