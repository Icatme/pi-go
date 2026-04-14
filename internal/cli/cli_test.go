package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/pkg/pigo"
)

func TestRunListShowsOAuthProviders(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Run(context.Background(), []string{"list"}, strings.NewReader(""), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d stderr=%q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "openai-codex") {
		t.Fatalf("expected openai-codex in list output, got %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "kimi-coding") {
		t.Fatalf("did not expect kimi-coding in oauth provider list, got %q", stdout.String())
	}
}

func TestRunUnknownCommandReturnsError(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := Run(context.Background(), []string{"wat"}, strings.NewReader(""), &stdout, &stderr)
	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
	if !strings.Contains(stderr.String(), "Unknown command: wat") {
		t.Fatalf("expected unknown command error, got %q", stderr.String())
	}
}

func TestRunModelsShowsProviderModels(t *testing.T) {
	provider := pigo.Provider("cli-models-provider")
	api := pigo.API("cli-models-api")

	pigo.RegisterProviderModule(pigo.ProviderModule{
		Provider: provider,
		Models: map[string]pigo.Model{
			"cli-model-a": {ID: "cli-model-a", API: api},
			"cli-model-b": {ID: "cli-model-b", API: api},
		},
	})
	pigo.RegisterAPIModule(pigo.APIModule{API: api})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Run(context.Background(), []string{"models", string(provider)}, strings.NewReader(""), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d stderr=%q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "cli-model-a") || !strings.Contains(stdout.String(), "cli-model-b") {
		t.Fatalf("expected provider models in output, got %q", stdout.String())
	}
}

func TestRunAskUsesStoredOAuthCredentials(t *testing.T) {
	provider := pigo.Provider("cli-oauth-provider")
	api := pigo.API("cli-oauth-api")
	modelID := "cli-oauth-model"

	pigo.RegisterProviderModule(pigo.ProviderModule{
		Provider: provider,
		Auth: pigo.ProviderAuth{
			RequiresOAuth: true,
		},
		Models: map[string]pigo.Model{
			modelID: {ID: modelID, API: api},
		},
	})
	pigo.RegisterAPIModule(pigo.APIModule{API: api})

	tempDir := t.TempDir()
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(previousDir)
	}()

	if err := saveAuth(filepath.Join(tempDir, ".pigo", "auth.json"), map[string]storedOAuthCredentials{
		string(provider): {
			Type:    "oauth",
			Access:  "token-123",
			Refresh: "refresh-123",
			Expires: 1710000000123,
		},
	}); err != nil {
		t.Fatalf("saveAuth failed: %v", err)
	}

	previousComplete := completeSimpleFn
	defer func() {
		completeSimpleFn = previousComplete
	}()
	completeSimpleFn = func(model pigo.Model, ctx pigo.Context, options pigo.SimpleStreamOptions) pigo.AssistantMessage {
		accessToken := ""
		if config, ok := options.Auth[provider]; ok && config.OAuth != nil {
			accessToken = config.OAuth.AccessToken
		}
		return pigo.AssistantMessage{
			API:        model.API,
			Provider:   model.Provider,
			Model:      model.ID,
			StopReason: pigo.StopReasonStop,
			Content: []pigo.ContentBlock{
				pigo.TextContent{Text: "oauth:" + accessToken},
			},
		}
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Run(context.Background(), []string{"ask", "--provider", string(provider), "--model", modelID, "hello"}, strings.NewReader(""), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d stderr=%q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "oauth:token-123") {
		t.Fatalf("expected oauth-backed output, got %q", stdout.String())
	}
}

func TestRunAskUsesAPIKey(t *testing.T) {
	provider := pigo.Provider("cli-api-provider")
	api := pigo.API("cli-api-only")
	modelID := "cli-api-model"

	pigo.RegisterProviderModule(pigo.ProviderModule{
		Provider: provider,
		Auth: pigo.ProviderAuth{
			EnvAPIKeyName: "CLI_TEST_API_KEY",
		},
		Models: map[string]pigo.Model{
			modelID: {ID: modelID, API: api},
		},
	})
	pigo.RegisterAPIModule(pigo.APIModule{API: api})

	t.Setenv("CLI_TEST_API_KEY", "env-key-123")

	previousComplete := completeSimpleFn
	defer func() {
		completeSimpleFn = previousComplete
	}()
	completeSimpleFn = func(model pigo.Model, ctx pigo.Context, options pigo.SimpleStreamOptions) pigo.AssistantMessage {
		return pigo.AssistantMessage{
			API:        model.API,
			Provider:   model.Provider,
			Model:      model.ID,
			StopReason: pigo.StopReasonStop,
			Content: []pigo.ContentBlock{
				pigo.TextContent{Text: "api:" + options.APIKey},
			},
		}
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Run(context.Background(), []string{"ask", "--provider", string(provider), "--model", modelID, "hello"}, strings.NewReader(""), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d stderr=%q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "api:env-key-123") {
		t.Fatalf("expected env api key output, got %q", stdout.String())
	}
}

func TestRunAskAppliesDefaultSystemPromptForDefaultCodexModel(t *testing.T) {
	previousComplete := completeSimpleFn
	previousGetModel := getModelFn
	defer func() {
		completeSimpleFn = previousComplete
		getModelFn = previousGetModel
	}()

	getModelFn = func(provider pigo.Provider, modelID string) *pigo.Model {
		if provider != "openai-codex" || modelID != "gpt-5.4" {
			return nil
		}
		return &pigo.Model{
			ID:       "gpt-5.4",
			Provider: "openai-codex",
			API:      "openai-codex-responses",
		}
	}

	capturedSystem := ""
	completeSimpleFn = func(model pigo.Model, ctx pigo.Context, options pigo.SimpleStreamOptions) pigo.AssistantMessage {
		capturedSystem = ctx.SystemPrompt
		return pigo.AssistantMessage{
			API:        model.API,
			Provider:   model.Provider,
			Model:      model.ID,
			StopReason: pigo.StopReasonStop,
			Content: []pigo.ContentBlock{
				pigo.TextContent{Text: "ok"},
			},
		}
	}

	tempDir := t.TempDir()
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}
	defer func() {
		_ = os.Chdir(previousDir)
	}()

	if err := saveAuth(filepath.Join(tempDir, ".pigo", "auth.json"), map[string]storedOAuthCredentials{
		"openai-codex": {
			Type:    "oauth",
			Access:  "token-123",
			Refresh: "refresh-123",
			Expires: 1710000000123,
		},
	}); err != nil {
		t.Fatalf("saveAuth failed: %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Run(context.Background(), []string{"ask", "hello"}, strings.NewReader(""), &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d stderr=%q", exitCode, stderr.String())
	}
	if capturedSystem != "You are a helpful assistant. Answer directly and concisely." {
		t.Fatalf("expected default system prompt, got %q", capturedSystem)
	}
}
