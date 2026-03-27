package pigo

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type openAICodexTestAuthFile struct {
	OpenAICodex openAICodexTestAuthEntry `json:"openai-codex"`
}

type openAICodexTestAuthEntry struct {
	Type        string `json:"type"`
	AccessToken string `json:"access"`
}

func loadOpenAICodexTestToken(path string) string {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	var payload openAICodexTestAuthFile
	if err := json.Unmarshal(bytes, &payload); err != nil {
		return ""
	}
	if payload.OpenAICodex.Type != "oauth" {
		return ""
	}
	return payload.OpenAICodex.AccessToken
}

func TestCompleteSimpleOpenAICodexLive(t *testing.T) {
	if os.Getenv("PIGO_LIVE_TEST") != "1" {
		t.Skip("set PIGO_LIVE_TEST=1 to run live openai-codex test")
	}

	token := loadOpenAICodexTestToken("01_auth.json")
	if token == "" {
		t.Skip("missing test-only openai codex token in 01_auth.json")
	}

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected openai-codex model")
	}

	response := CompleteSimple(*model, Context{
		SystemPrompt: "Reply with exactly OK.",
		Messages: []Message{
			UserMessage{Content: "Say OK"},
		},
	}, CompleteOptions{
		APIKey: token,
	})

	if response.StopReason != StopReasonStop {
		t.Fatalf("expected live stop reason stop, got %q with error %q", response.StopReason, response.ErrorMessage)
	}
	if len(response.Content) == 0 {
		t.Fatal("expected content from live openai codex response")
	}
	if response.ResponseID == "" {
		t.Fatal("expected live codex response id")
	}

	text, ok := response.Content[0].(TextContent)
	if !ok {
		t.Fatalf("expected first content block to be text, got %T", response.Content[0])
	}
	if !strings.Contains(strings.ToUpper(text.Text), "OK") {
		t.Fatalf("expected live codex response to contain OK, got %q", text.Text)
	}
}
