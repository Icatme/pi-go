package pigo

import (
	"testing"
)

func TestRepairJSONEscapesControlCharacters(t *testing.T) {
	input := `{"text": "hello` + "\n" + `world"}`
	repaired := repairJSON(input)
	expected := `{"text": "hello\nworld"}`
	if repaired != expected {
		t.Fatalf("expected %q, got %q", expected, repaired)
	}
}

func TestRepairJSONPreservesDoubleBackslash(t *testing.T) {
	input := `{"path": "C:\\users\\test"}`
	repaired := repairJSON(input)
	if repaired != input {
		t.Fatalf("expected unchanged valid JSON, got %q", repaired)
	}
}

func TestRepairJSONDoublesSingleBackslashBeforeInvalidEscape(t *testing.T) {
	input := `{"text": "hello\world"}`
	repaired := repairJSON(input)
	expected := `{"text": "hello\\world"}`
	if repaired != expected {
		t.Fatalf("expected %q, got %q", expected, repaired)
	}
}

func TestRepairJSONPreservesValidEscapes(t *testing.T) {
	input := `{"text": "hello\nworld\t!"}`
	repaired := repairJSON(input)
	if repaired != input {
		t.Fatalf("expected unchanged valid JSON, got %q", repaired)
	}
}

func TestRepairJSONPreservesUnicodeEscapes(t *testing.T) {
	input := `{"emoji": "\u2764"}`
	repaired := repairJSON(input)
	if repaired != input {
		t.Fatalf("expected unchanged unicode escape, got %q", repaired)
	}
}

func TestParseJSONWithRepairValidJSON(t *testing.T) {
	input := `{"name": "test", "value": 42}`
	result, err := parseJSONWithRepair[map[string]any](input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["name"] != "test" {
		t.Fatalf("expected name 'test', got %v", result["name"])
	}
}

func TestParseJSONWithRepairRepairsAndParses(t *testing.T) {
	input := `{"text": "hello` + "\n" + `world"}`
	result, err := parseJSONWithRepair[map[string]any](input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["text"] != "hello\nworld" {
		t.Fatalf("expected repaired text, got %v", result["text"])
	}
}

func TestParseJSONWithRepairReturnsErrorForInvalidJSON(t *testing.T) {
	input := `{"broken"`
	_, err := parseJSONWithRepair[map[string]any](input)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseStreamingJSONValidJSON(t *testing.T) {
	input := `{"name": "test"}`
	result := parseStreamingJSON(input)
	if result["name"] != "test" {
		t.Fatalf("expected name 'test', got %v", result["name"])
	}
}

func TestParseStreamingJSONRepairsAndParses(t *testing.T) {
	input := `{"text": "hello` + "\n" + `world"}`
	result := parseStreamingJSON(input)
	if result["text"] != "hello\nworld" {
		t.Fatalf("expected repaired text, got %v", result["text"])
	}
}

func TestParseStreamingJSONReturnsEmptyMapForInvalidInput(t *testing.T) {
	input := `{"broken"`
	result := parseStreamingJSON(input)
	if result == nil {
		t.Fatal("expected non-nil empty map")
	}
	if len(result) != 0 {
		t.Fatalf("expected empty map, got %v", result)
	}
}

func TestParseStreamingJSONReturnsEmptyMapForEmptyInput(t *testing.T) {
	result := parseStreamingJSON("")
	if result == nil {
		t.Fatal("expected non-nil empty map")
	}
	if len(result) != 0 {
		t.Fatalf("expected empty map, got %v", result)
	}
}

func TestParseStreamingJSONReturnsEmptyMapForWhitespaceInput(t *testing.T) {
	result := parseStreamingJSON("   \n\t  ")
	if result == nil {
		t.Fatal("expected non-nil empty map")
	}
	if len(result) != 0 {
		t.Fatalf("expected empty map, got %v", result)
	}
}
