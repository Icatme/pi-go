package pigo

import (
	"strings"
	"testing"
)

func TestValidateToolArgumentsFallsBackToRawArgumentsWithoutSchemaOrValidator(t *testing.T) {
	tool := Tool{
		Name:        "echo",
		Description: "Echo tool",
	}
	toolCall := ToolCall{
		ID:        "tool-1",
		Name:      "echo",
		Arguments: map[string]any{"count": "42"},
	}

	validated, err := ValidateToolArguments(tool, toolCall)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if got := validated["count"]; got != "42" {
		t.Fatalf("expected raw argument to be preserved, got %#v", got)
	}
}

func TestValidateToolArgumentsValidatesSchemaAndCoercesValues(t *testing.T) {
	tool := Tool{
		Name:        "echo",
		Description: "Echo tool",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"count":   map[string]any{"type": "number"},
				"enabled": map[string]any{"type": "boolean"},
				"nested": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"age": map[string]any{"type": "integer"},
					},
					"required": []string{"age"},
				},
			},
			"required": []string{"count", "enabled", "nested"},
		},
	}
	toolCall := ToolCall{
		ID:   "tool-1",
		Name: "echo",
		Arguments: map[string]any{
			"count":   "42",
			"enabled": "true",
			"nested": map[string]any{
				"age": "7",
			},
		},
	}

	validated, err := ValidateToolArguments(tool, toolCall)
	if err != nil {
		t.Fatalf("expected valid coerced arguments, got error: %v", err)
	}
	if got, ok := validated["count"].(float64); !ok || got != 42 {
		t.Fatalf("expected count to coerce to number 42, got %#v", validated["count"])
	}
	if got, ok := validated["enabled"].(bool); !ok || !got {
		t.Fatalf("expected enabled to coerce to bool true, got %#v", validated["enabled"])
	}
	nested, ok := validated["nested"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested object after validation, got %#v", validated["nested"])
	}
	if got, ok := nested["age"].(int); !ok || got != 7 {
		t.Fatalf("expected nested age to coerce to integer 7, got %#v", nested["age"])
	}
}

func TestValidateToolArgumentsReturnsFormattedErrors(t *testing.T) {
	tool := Tool{
		Name:        "echo",
		Description: "Echo tool",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"count": map[string]any{"type": "number"},
			},
			"required": []string{"count"},
		},
	}
	toolCall := ToolCall{
		ID:        "tool-1",
		Name:      "echo",
		Arguments: map[string]any{},
	}

	_, err := ValidateToolArguments(tool, toolCall)
	if err == nil {
		t.Fatal("expected validation error for missing required field")
	}
	message := err.Error()
	if !strings.Contains(message, `Validation failed for tool "echo":`) {
		t.Fatalf("expected formatted validation header, got %q", message)
	}
	if !strings.Contains(message, "count: must have required property") {
		t.Fatalf("expected missing-property path in error, got %q", message)
	}
	if !strings.Contains(message, "Received arguments:\n{}") {
		t.Fatalf("expected received arguments payload in error, got %q", message)
	}
}

func TestValidateToolArgumentsUsesCustomValidatorWhenProvided(t *testing.T) {
	tool := Tool{
		Name: "echo",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"count": map[string]any{"type": "number"},
			},
		},
		Validator: ToolArgumentsValidatorFunc(func(args map[string]any) (map[string]any, error) {
			args["count"] = "custom"
			return args, nil
		}),
	}
	toolCall := ToolCall{
		ID:        "tool-1",
		Name:      "echo",
		Arguments: map[string]any{"count": "42"},
	}

	validated, err := ValidateToolArguments(tool, toolCall)
	if err != nil {
		t.Fatalf("expected custom validator to succeed, got %v", err)
	}
	if got := validated["count"]; got != "custom" {
		t.Fatalf("expected custom validator result, got %#v", got)
	}
}

func TestValidateToolCallFindsToolByName(t *testing.T) {
	tools := []Tool{
		{
			Name: "double_number",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"value": map[string]any{"type": "number"},
				},
				"required": []string{"value"},
			},
		},
	}
	toolCall := ToolCall{
		ID:        "tool-1",
		Name:      "double_number",
		Arguments: map[string]any{"value": "21"},
	}

	validated, err := ValidateToolCall(tools, toolCall)
	if err != nil {
		t.Fatalf("expected tool lookup validation to succeed, got %v", err)
	}
	if got, ok := validated["value"].(float64); !ok || got != 21 {
		t.Fatalf("expected validated number 21, got %#v", validated["value"])
	}
}

func TestValidateToolCallErrorsForUnknownTool(t *testing.T) {
	_, err := ValidateToolCall(nil, ToolCall{
		ID:   "tool-1",
		Name: "missing_tool",
	})
	if err == nil {
		t.Fatal("expected missing tool error")
	}
	if !strings.Contains(err.Error(), `Tool "missing_tool" not found`) {
		t.Fatalf("expected missing tool error message, got %q", err.Error())
	}
}

func TestValidateToolArgumentsEnforcesStringPatternAndLength(t *testing.T) {
	tool := Tool{
		Name: "echo",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":      "string",
					"minLength": 3,
					"maxLength": 5,
					"pattern":   "^[a-z]+$",
				},
			},
			"required": []string{"name"},
		},
	}

	_, err := ValidateToolArguments(tool, ToolCall{
		ID:        "tool-1",
		Name:      "echo",
		Arguments: map[string]any{"name": "AB"},
	})
	if err == nil {
		t.Fatal("expected string constraint validation error")
	}
	if !strings.Contains(err.Error(), "name: must NOT have fewer than 3 characters") &&
		!strings.Contains(err.Error(), `name: must match pattern "^[a-z]+$"`) {
		t.Fatalf("expected string constraint error, got %q", err.Error())
	}
}

func TestValidateToolArgumentsEnforcesNumberRange(t *testing.T) {
	tool := Tool{
		Name: "range_tool",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{
					"type":             "number",
					"exclusiveMinimum": 0,
					"maximum":          10,
				},
			},
			"required": []string{"value"},
		},
	}

	_, err := ValidateToolArguments(tool, ToolCall{
		ID:        "tool-1",
		Name:      "range_tool",
		Arguments: map[string]any{"value": "0"},
	})
	if err == nil {
		t.Fatal("expected number range validation error")
	}
	if !strings.Contains(err.Error(), "value: must be > 0") {
		t.Fatalf("expected exclusiveMinimum error, got %q", err.Error())
	}
}

func TestValidateToolArgumentsEnforcesArrayAndAdditionalProperties(t *testing.T) {
	tool := Tool{
		Name: "array_tool",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"items": map[string]any{
					"type":     "array",
					"minItems": 2,
					"items": map[string]any{
						"type": "integer",
					},
				},
			},
			"required":             []string{"items"},
			"additionalProperties": false,
		},
	}

	_, err := ValidateToolArguments(tool, ToolCall{
		ID:   "tool-1",
		Name: "array_tool",
		Arguments: map[string]any{
			"items": []any{"1"},
			"extra": true,
		},
	})
	if err == nil {
		t.Fatal("expected array/additionalProperties validation error")
	}
	message := err.Error()
	if !strings.Contains(message, "items: must NOT have fewer than 2 items") &&
		!strings.Contains(message, "extra: must NOT have additional properties") {
		t.Fatalf("expected array/additionalProperties error, got %q", message)
	}
}

func TestValidateToolArgumentsSupportsAllOf(t *testing.T) {
	tool := Tool{
		Name: "allof_tool",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{
					"allOf": []any{
						map[string]any{"type": "string", "minLength": 3},
						map[string]any{"pattern": "^[a-z]+$"},
					},
				},
			},
			"required": []string{"value"},
		},
	}

	validated, err := ValidateToolArguments(tool, ToolCall{
		ID:        "tool-1",
		Name:      "allof_tool",
		Arguments: map[string]any{"value": "abc"},
	})
	if err != nil {
		t.Fatalf("expected allOf validation to succeed, got %v", err)
	}
	if validated["value"] != "abc" {
		t.Fatalf("expected validated value abc, got %#v", validated["value"])
	}
}

func TestValidateToolArgumentsCoercesPrimitiveTypes(t *testing.T) {
	passingCases := []struct {
		schemaType string
		input      any
		expected   any
	}{
		{"number", "42", float64(42)},
		{"number", true, float64(1)},
		{"number", nil, float64(0)},
		{"integer", "42", int(42)},
		{"boolean", "true", true},
		{"boolean", "false", false},
		{"boolean", 1, true},
		{"boolean", 0, false},
		{"string", nil, ""},
		{"string", true, "true"},
		{"null", "", nil},
		{"null", 0, nil},
		{"null", false, nil},
	}

	for _, tc := range passingCases {
		tool := Tool{
			Name: "coerce_tool",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"value": map[string]any{"type": tc.schemaType},
				},
				"required": []string{"value"},
			},
		}
		toolCall := ToolCall{
			ID:        "tool-1",
			Name:      "coerce_tool",
			Arguments: map[string]any{"value": tc.input},
		}

		validated, err := ValidateToolArguments(tool, toolCall)
		if err != nil {
			t.Fatalf("expected coercion for %s from %#v to succeed, got error: %v", tc.schemaType, tc.input, err)
		}
		if validated["value"] != tc.expected {
			t.Fatalf("expected %s coercion from %#v to produce %#v, got %#v", tc.schemaType, tc.input, tc.expected, validated["value"])
		}
	}
}

func TestValidateToolArgumentsRejectsInvalidCoercions(t *testing.T) {
	failingCases := []struct {
		schemaType string
		input      any
	}{
		{"boolean", "1"},
		{"boolean", "0"},
		{"null", "null"},
		{"integer", "42.1"},
	}

	for _, tc := range failingCases {
		tool := Tool{
			Name: "reject_tool",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"value": map[string]any{"type": tc.schemaType},
				},
				"required": []string{"value"},
			},
		}
		toolCall := ToolCall{
			ID:        "tool-1",
			Name:      "reject_tool",
			Arguments: map[string]any{"value": tc.input},
		}

		_, err := ValidateToolArguments(tool, toolCall)
		if err == nil {
			t.Fatalf("expected %s coercion from %#v to fail", tc.schemaType, tc.input)
		}
	}
}

func TestValidateToolArgumentsCoercesUnionType(t *testing.T) {
	tool := Tool{
		Name: "union_tool",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{
					"type": []any{"number", "string"},
				},
			},
			"required": []string{"value"},
		},
	}

	validated, err := ValidateToolArguments(tool, ToolCall{
		ID:        "tool-1",
		Name:      "union_tool",
		Arguments: map[string]any{"value": "1"},
	})
	if err != nil {
		t.Fatalf("expected union type validation to succeed, got error: %v", err)
	}
	if validated["value"] != "1" {
		t.Fatalf("expected union type to preserve string match, got %#v", validated["value"])
	}
}

func TestValidateToolArgumentsSupportsAnyOf(t *testing.T) {
	tool := Tool{
		Name: "anyof_tool",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{
					"anyOf": []any{
						map[string]any{"type": "string"},
						map[string]any{"type": "number"},
					},
				},
			},
			"required": []string{"value"},
		},
	}

	// String should match first anyOf branch
	validated, err := ValidateToolArguments(tool, ToolCall{
		ID:        "tool-1",
		Name:      "anyof_tool",
		Arguments: map[string]any{"value": "hello"},
	})
	if err != nil {
		t.Fatalf("expected anyOf string validation to succeed, got error: %v", err)
	}
	if validated["value"] != "hello" {
		t.Fatalf("expected anyOf to preserve string, got %#v", validated["value"])
	}

	// Number input with anyOf[string, number]: string branch comes first and coerces int to string
	// This is expected behavior - anyOf returns the first matching branch
	validated, err = ValidateToolArguments(tool, ToolCall{
		ID:        "tool-2",
		Name:      "anyof_tool",
		Arguments: map[string]any{"value": 42},
	})
	if err != nil {
		t.Fatalf("expected anyOf number validation to succeed, got error: %v", err)
	}
	// int 42 gets coerced to "42" by the first (string) branch
	if validated["value"] != "42" {
		t.Fatalf("expected anyOf to coerce int 42 to string '42' via first branch, got %#v", validated["value"])
	}
}

func TestValidateToolArgumentsSupportsOneOf(t *testing.T) {
	tool := Tool{
		Name: "oneof_tool",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{
					"oneOf": []any{
						map[string]any{"type": "string", "minLength": 5},
						map[string]any{"type": "integer"},
					},
				},
			},
			"required": []string{"value"},
		},
	}

	// Integer should match second oneOf branch exclusively
	validated, err := ValidateToolArguments(tool, ToolCall{
		ID:        "tool-1",
		Name:      "oneof_tool",
		Arguments: map[string]any{"value": "42"},
	})
	if err != nil {
		t.Fatalf("expected oneOf integer validation to succeed, got error: %v", err)
	}
	if got, ok := validated["value"].(int); !ok || got != 42 {
		t.Fatalf("expected oneOf to coerce to integer 42, got %#v", validated["value"])
	}
}

func TestValidateToolArgumentsEnumBeforeType(t *testing.T) {
	tool := Tool{
		Name: "enum_tool",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]any{
					"type": "string",
					"enum": []any{"active", "inactive", "pending"},
				},
			},
			"required": []string{"status"},
		},
	}

	// Valid enum value should pass
	validated, err := ValidateToolArguments(tool, ToolCall{
		ID:        "tool-1",
		Name:      "enum_tool",
		Arguments: map[string]any{"status": "active"},
	})
	if err != nil {
		t.Fatalf("expected enum validation to succeed, got error: %v", err)
	}
	if validated["status"] != "active" {
		t.Fatalf("expected enum value to be preserved, got %#v", validated["status"])
	}

	// Invalid enum value should fail
	_, err = ValidateToolArguments(tool, ToolCall{
		ID:        "tool-2",
		Name:      "enum_tool",
		Arguments: map[string]any{"status": "deleted"},
	})
	if err == nil {
		t.Fatal("expected enum validation to fail for invalid value")
	}
}
