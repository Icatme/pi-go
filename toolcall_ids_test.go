package pigo

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

const copilotRawToolCallID = "call_4VnzVawQXPB9MgYib7CiQFEY|I9b95oN1wD/cHXKTw3PpRkL6KkCtzTJhUxMouMWYwHeTo2j3htzfSk7YPx2vifiIM4g3A8XXyOj8q4Bt6SLUG7gqY1E3ELkrkVQNHglRfUmWj84lqxJY+Puieb3VKyX0FB+83TUzn91cDMF/4gzt990IzqVrc+nIb9RRscRD070Du16q1glydVjWR0SBJsE6TbY/esOjFpqplogQqrajm1eI++f3eLi73R6q7hVusY0QbeFySVxABCjhN0lXB04caBe1rzHjYzul6MAXj7uq+0r17VLq+yrtyYhN12wkmFqHeqTyEei6EFPbMy24Nc+IbJlkP0OCg02W+gOnyBFcbi2ctvJFSOhSjt1CqBdqCnnhwUqXjbWiT0wh3DmLScRgTHmGkaI+oAcQQjfic65nxj+TnEkReA=="

func TestNormalizeOpenAIResponsesToolCallIDHashesForeignIDsForCodex(t *testing.T) {
	model := GetModel("openai-codex", "gpt-5.3-codex")
	if model == nil {
		t.Fatal("expected gpt-5.3-codex model to exist")
	}

	source := AssistantMessage{
		API:        "openai-responses",
		Provider:   "github-copilot",
		Model:      "gpt-5.3-codex",
		StopReason: StopReasonToolUse,
		Timestamp:  time.Now().UTC(),
	}

	normalized := NormalizeOpenAIResponsesToolCallID(copilotRawToolCallID, *model, source)
	parts := strings.SplitN(normalized, "|", 2)
	if len(parts) != 2 {
		t.Fatalf("expected normalized id to keep call_id|item_id shape, got %q", normalized)
	}

	expectedItemID := "fc_" + ShortHash(strings.SplitN(copilotRawToolCallID, "|", 2)[1])
	if parts[1] != expectedItemID {
		t.Fatalf("expected hashed item id %q, got %q", expectedItemID, parts[1])
	}
	if len(parts[1]) > 64 {
		t.Fatalf("expected item id <= 64 chars, got %d", len(parts[1]))
	}
	if !regexp.MustCompile(`^fc_[A-Za-z0-9]+$`).MatchString(parts[1]) {
		t.Fatalf("expected codex-safe item id, got %q", parts[1])
	}
}

func TestNormalizeOpenAIResponsesToolCallIDNormalizesSameProviderIDs(t *testing.T) {
	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected gpt-5.4 model to exist")
	}

	source := AssistantMessage{
		API:        "openai-codex-responses",
		Provider:   "openai-codex",
		Model:      "gpt-5.4",
		StopReason: StopReasonToolUse,
		Timestamp:  time.Now().UTC(),
	}

	normalized := NormalizeOpenAIResponsesToolCallID("call/123|abc+def==", *model, source)
	if normalized != "call_123|fc_abc_def" {
		t.Fatalf("expected normalized same-provider id, got %q", normalized)
	}
}

func TestNormalizeSimpleToolCallIDSanitizesAndBounds(t *testing.T) {
	normalized := NormalizeSimpleToolCallID("id with spaces/+==")
	if normalized != "id_with_spaces" {
		t.Fatalf("expected sanitized id, got %q", normalized)
	}

	longID := strings.Repeat("a", 80)
	if got := NormalizeSimpleToolCallID(longID); len(got) != 64 {
		t.Fatalf("expected bounded id length 64, got %d", len(got))
	}
}
