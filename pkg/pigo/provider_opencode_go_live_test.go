package pigo

import (
	"os"
	"strings"
	"testing"
)

func TestCompleteSimpleOpenCodeGoLive(t *testing.T) {
	if os.Getenv("PIGO_LIVE_TEST") != "1" {
		t.Skip("set PIGO_LIVE_TEST=1 to run live opencode-go test")
	}

	apiKey := GetEnvAPIKey("opencode-go")
	if apiKey == "" {
		t.Skip("missing OPENCODE_API_KEY for live test")
	}
	model := GetModel("opencode-go", "kimi-k2.6")
	if model == nil {
		t.Fatal("expected OpenCode Go default model")
	}

	response := CompleteSimple(*model, Context{
		SystemPrompt: "Reply with exactly OK.",
		Messages:     []Message{UserMessage{Content: "Say OK"}},
	}, SimpleStreamOptions{APIKey: apiKey, MaxTokens: 64})
	if response.StopReason != StopReasonStop {
		t.Fatalf("expected live stop reason stop, got %q with error %q", response.StopReason, response.ErrorMessage)
	}
	if response.ResponseID == "" {
		t.Fatal("expected live OpenCode Go response id")
	}
	for _, block := range response.Content {
		if text, ok := block.(TextContent); ok && strings.Contains(strings.ToUpper(text.Text), "OK") {
			return
		}
	}
	t.Fatalf("expected live OpenCode Go response to contain OK, got %#v", response.Content)
}
