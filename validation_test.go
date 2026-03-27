package pigo

import "testing"

func TestValidateToolArgumentsFallsBackToRawArgumentsWithoutValidator(t *testing.T) {
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
