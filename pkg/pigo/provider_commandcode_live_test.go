package pigo

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestCompleteSimpleCommandCodeLive(t *testing.T) {
	if os.Getenv("PIGO_LIVE_TEST") != "1" {
		t.Skip("set PIGO_LIVE_TEST=1 to run live Command Code tests")
	}
	apiKey := ResolveCommandCodeAPIKey(nil)
	if apiKey == "" {
		t.Skip("Command Code credentials are not configured")
	}
	models, err := RefreshCommandCodeModels(context.Background(), nil)
	if err != nil {
		t.Fatalf("refresh Command Code models: %v", err)
	}
	var model *Model
	for index := range models {
		if strings.HasSuffix(models[index].ID, "-free") {
			selected := models[index]
			model = &selected
			break
		}
	}
	if model == nil {
		t.Skip("Command Code currently advertises no explicitly free model")
	}

	response := CompleteSimple(*model, Context{
		SystemPrompt: "Reply with exactly OK.",
		Messages:     []Message{UserMessage{Content: "Reply with exactly OK."}},
	}, SimpleStreamOptions{
		APIKey:    apiKey,
		MaxTokens: 16,
		TimeoutMs: 30_000,
	})
	if response.StopReason == StopReasonError || response.StopReason == StopReasonAborted {
		t.Fatalf("live Command Code request failed: stop=%s error=%s", response.StopReason, response.ErrorMessage)
	}
	if len(response.Content) == 0 {
		t.Fatalf("live Command Code request returned no content: %+v", response)
	}
}
