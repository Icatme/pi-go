package pigo

import "testing"

func TestSupportsXHighForAnthropicOpus46(t *testing.T) {
	model := GetModel("anthropic", "claude-opus-4-6")
	if model == nil {
		t.Fatal("expected model to exist")
	}
	if !SupportsXHigh(*model) {
		t.Fatal("expected opus 4.6 to support xhigh")
	}
}

func TestSupportsXHighFalseForNonOpusAnthropic(t *testing.T) {
	model := GetModel("anthropic", "claude-sonnet-4-5")
	if model == nil {
		t.Fatal("expected model to exist")
	}
	if SupportsXHigh(*model) {
		t.Fatal("expected claude-sonnet-4-5 to not support xhigh")
	}
}

func TestSupportsXHighForGPT54(t *testing.T) {
	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected model to exist")
	}
	if !SupportsXHigh(*model) {
		t.Fatal("expected gpt-5.4 to support xhigh")
	}
}

func TestSupportsXHighForOpenRouterOpus46(t *testing.T) {
	model := GetModel("openrouter", "anthropic/claude-opus-4.6")
	if model == nil {
		t.Fatal("expected model to exist")
	}
	if !SupportsXHigh(*model) {
		t.Fatal("expected openrouter opus 4.6 to support xhigh")
	}
}
