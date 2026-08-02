package pigo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchCommandCodeModelsMatchesProviderAPI(t *testing.T) {
	var accept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		accept = request.Header.Get("Accept")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []any{
				map[string]any{"id": "gpt-5.4", "name": "GPT-5.4", "context_length": 400_000},
				map[string]any{"id": "new/model", "name": "New Model", "context_length": 32_000},
			},
		})
	}))
	defer server.Close()

	t.Setenv("COMMANDCODE_MODELS_URL", server.URL)
	t.Setenv("COMMANDCODE_API_BASE", "https://commandcode.example.test/root/")
	models, err := FetchCommandCodeModels(context.Background(), server.Client())
	if err != nil {
		t.Fatalf("FetchCommandCodeModels returned error: %v", err)
	}
	if accept != "application/json" {
		t.Fatalf("expected JSON accept header, got %q", accept)
	}
	if len(models) != 2 {
		t.Fatalf("expected two discovered models, got %+v", models)
	}
	known := models[0]
	if known.ID != "gpt-5.4" || known.Name != "GPT-5.4 (CC)" || !known.Reasoning || known.ContextWindow != 400_000 || known.MaxTokens != 65_536 {
		t.Fatalf("unexpected known model: %+v", known)
	}
	if known.BaseURL != "https://commandcode.example.test/root" || known.API != "commandcode-custom" || known.Provider != "commandcode" {
		t.Fatalf("unexpected transport metadata: %+v", known)
	}
	if known.Cost != commandCodeModelCosts[known.ID] {
		t.Fatalf("expected known pricing overlay, got %+v", known.Cost)
	}
	unknown := models[1]
	if unknown.ID != "new/model" || unknown.MaxTokens != 32_000 || unknown.Cost != (UsageCost{}) {
		t.Fatalf("expected upstream-compatible unknown-price model, got %+v", unknown)
	}
}

func TestCommandCodeAPIBaseOverrideAppliesToDefaultGenerateURL(t *testing.T) {
	t.Setenv("COMMANDCODE_API_BASE", "https://commandcode.example.test/custom/")
	if got := resolveCommandCodeGenerateURL(""); got != "https://commandcode.example.test/custom/alpha/generate" {
		t.Fatalf("unexpected generate URL %q", got)
	}
}

func TestRefreshCommandCodeModelsReplacesRegistrySnapshot(t *testing.T) {
	isolateProviderRegistry(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"live-only","name":"Live Only","context_length":100000}]}`))
	}))
	defer server.Close()
	t.Setenv("COMMANDCODE_MODELS_URL", server.URL)

	models, err := RefreshCommandCodeModels(context.Background(), server.Client())
	if err != nil {
		t.Fatalf("RefreshCommandCodeModels returned error: %v", err)
	}
	if len(models) != 1 || models[0].ID != "live-only" || models[0].Provider != "commandcode" {
		t.Fatalf("unexpected refreshed models: %+v", models)
	}
	if GetModel("commandcode", "live-only") == nil {
		t.Fatal("expected refreshed model lookup")
	}
	if GetModel("commandcode", "gpt-5.4") != nil {
		t.Fatal("expected stale snapshot model to be replaced")
	}
}

func TestFetchCommandCodeModelsRejectsInvalidCatalog(t *testing.T) {
	tests := map[string]string{
		"wrong object": `{"object":"model","data":[]}`,
		"missing id":   `{"object":"list","data":[{"name":"No ID","context_length":1000}]}`,
		"duplicate id": `{"object":"list","data":[{"id":"same","name":"One","context_length":1000},{"id":"same","name":"Two","context_length":1000}]}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			if _, err := fetchCommandCodeModelSpecs(context.Background(), server.Client(), server.URL); err == nil {
				t.Fatal("expected invalid catalog error")
			}
		})
	}
}

func TestReadCommandCodeAPIKeyMatchesExtensionAuthShapes(t *testing.T) {
	tests := map[string]string{
		"direct apiKey":      `{"apiKey":"direct-key"}`,
		"legacy provider":    `{"commandcode":"legacy-key"}`,
		"pi oauth":           `{"commandcode":{"type":"oauth","access":"oauth-key","refresh":"refresh-key"}}`,
		"official cli":       `{"command-code":{"type":"api","key":"official-key"}}`,
		"untyped key record": `{"command-code":{"key":"record-key"}}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			apiKey := readCommandCodeAPIKey([]string{path})
			if apiKey == "" || !strings.HasSuffix(apiKey, "key") {
				t.Fatalf("expected supported API key shape, got %q", apiKey)
			}
		})
	}
}

func TestReadCommandCodeAPIKeyUsesPathOrderAndIgnoresMalformedFiles(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	first := filepath.Join(dir, "first.json")
	second := filepath.Join(dir, "second.json")
	for path, body := range map[string]string{
		bad:    `not-json`,
		first:  `{"apiKey":"first-key"}`,
		second: `{"apiKey":"second-key"}`,
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := readCommandCodeAPIKey([]string{bad, first, second}); got != "first-key" {
		t.Fatalf("expected first readable credential, got %q", got)
	}
	if usableCommandCodeAPIKey("$COMMANDCODE_API_KEY") != "" || usableCommandCodeAPIKey("COMMANDCODE_API_KEY") != "" {
		t.Fatal("expected unresolved host environment references to be ignored")
	}
}
