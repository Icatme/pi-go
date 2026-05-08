package pigo

import (
	"errors"
	"testing"
)

func TestFormatThrownValueWithError(t *testing.T) {
	err := errors.New("something went wrong")
	if got := formatThrownValue(err); got != "something went wrong" {
		t.Fatalf("expected error message, got %q", got)
	}
}

func TestFormatThrownValueWithString(t *testing.T) {
	if got := formatThrownValue("plain string"); got != "plain string" {
		t.Fatalf("expected plain string, got %q", got)
	}
}

func TestFormatThrownValueWithOther(t *testing.T) {
	if got := formatThrownValue(42); got != "42" {
		t.Fatalf("expected '42', got %q", got)
	}
}

func TestExtractDiagnosticErrorWithError(t *testing.T) {
	err := errors.New("test error")
	info := extractDiagnosticError(err)
	if info.Message != "test error" {
		t.Fatalf("expected message 'test error', got %q", info.Message)
	}
	if info.Name == "" {
		t.Fatal("expected non-empty name for error type")
	}
}

func TestExtractDiagnosticErrorWithNonError(t *testing.T) {
	info := extractDiagnosticError("thrown string")
	if info.Name != "ThrownValue" {
		t.Fatalf("expected name 'ThrownValue', got %q", info.Name)
	}
	if info.Message != "thrown string" {
		t.Fatalf("expected message 'thrown string', got %q", info.Message)
	}
}

func TestCreateAssistantMessageDiagnostic(t *testing.T) {
	diag := createAssistantMessageDiagnostic("test-type", errors.New("test error"), map[string]any{"key": "value"})
	if diag.Type != "test-type" {
		t.Fatalf("expected type 'test-type', got %q", diag.Type)
	}
	if diag.Error.Message != "test error" {
		t.Fatalf("expected error message 'test error', got %q", diag.Error.Message)
	}
	if diag.Details["key"] != "value" {
		t.Fatalf("expected detail key 'value', got %v", diag.Details["key"])
	}
	if diag.Timestamp.IsZero() {
		t.Fatal("expected non-zero timestamp")
	}
}

func TestAppendAssistantMessageDiagnostic(t *testing.T) {
	msg := AssistantMessage{}
	if len(msg.Diagnostics) != 0 {
		t.Fatal("expected empty diagnostics initially")
	}

	diag1 := createAssistantMessageDiagnostic("first", errors.New("first error"), nil)
	appendAssistantMessageDiagnostic(&msg, diag1)
	if len(msg.Diagnostics) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d", len(msg.Diagnostics))
	}

	diag2 := createAssistantMessageDiagnostic("second", errors.New("second error"), nil)
	appendAssistantMessageDiagnostic(&msg, diag2)
	if len(msg.Diagnostics) != 2 {
		t.Fatalf("expected 2 diagnostics, got %d", len(msg.Diagnostics))
	}
	if msg.Diagnostics[1].Type != "second" {
		t.Fatalf("expected second diagnostic type 'second', got %q", msg.Diagnostics[1].Type)
	}
}
