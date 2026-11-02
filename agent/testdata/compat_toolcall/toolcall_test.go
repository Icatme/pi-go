// This external-package fixture intentionally uses the public pre-Kiro layout.
// Run explicitly from the repository root; testdata is not included in ./....
// It must compile after the source-compatibility blocker is resolved.
package compat_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

var _ = agent.ToolCall{"id", "original", "lookup", "namespace", nil, nil, "signature"}

// Tags are part of assignment identity, even though explicit conversions
// between otherwise identical structs ignore tags. Keep the migration honest.
func TestLegacyTagRequiresExplicitConversion(t *testing.T) {
	type legacyShape = struct {
		ID               string          `json:"id"`
		OriginalID       string          `json:"original_id,omitempty"`
		Name             string          `json:"name"`
		Namespace        string          `json:"namespace,omitempty"`
		Arguments        json.RawMessage `json:"arguments,omitempty"`
		ParsedArgs       map[string]any  `json:"parsed_args,omitempty"`
		ThoughtSignature string          `json:"thought_signature,omitempty"`
	}
	current := agent.ToolCall{ID: "call", ParsedArgs: map[string]any{}}
	if reflect.TypeOf(current).AssignableTo(reflect.TypeFor[legacyShape]()) {
		t.Fatal("legacy tag unexpectedly assignment-compatible")
	}
	legacy := legacyShape(current)
	if legacy.ID != current.ID || legacy.ParsedArgs == nil {
		t.Fatal("explicit field-layout conversion lost values")
	}
}
