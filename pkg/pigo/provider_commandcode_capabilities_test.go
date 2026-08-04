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

func TestResolveCommandCodeModelsCachePathMatchesPluginOverrides(t *testing.T) {
	direct := filepath.Join(t.TempDir(), "direct-cache.json")
	t.Setenv("COMMANDCODE_MODELS_CACHE", direct)
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(t.TempDir(), "ignored-agent"))
	path, err := resolveCommandCodeModelsCachePath()
	if err != nil || path != direct {
		t.Fatalf("expected explicit cache path, path=%q err=%v", path, err)
	}

	t.Setenv("COMMANDCODE_MODELS_CACHE", "")
	agentDir := filepath.Join(t.TempDir(), "agent")
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	path, err = resolveCommandCodeModelsCachePath()
	if err != nil || path != filepath.Join(agentDir, commandCodeModelsCacheFileName) {
		t.Fatalf("expected agent-dir cache path, path=%q err=%v", path, err)
	}
}

func TestRefreshCommandCodeModelsReplacesRegistrySnapshot(t *testing.T) {
	isolateProviderRegistry(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"live-only","name":"Live Only","context_length":100000}]}`))
	}))
	defer server.Close()
	t.Setenv("COMMANDCODE_MODELS_URL", server.URL)
	t.Setenv("COMMANDCODE_MODELS_CACHE", filepath.Join(t.TempDir(), "commandcode-models.json"))

	result, err := RefreshCommandCodeModelsWithResult(context.Background(), server.Client())
	if err != nil {
		t.Fatalf("RefreshCommandCodeModelsWithResult returned error: %v", err)
	}
	models := result.Models
	if result.Source != CommandCodeModelsSourceLive || result.Warning != "" {
		t.Fatalf("unexpected refresh result: %+v", result)
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

func TestLoadCommandCodeModelsWritesAndReusesPluginCompatibleCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"cached-model","name":"Cached Model","context_length":100000}]}`))
	}))
	t.Cleanup(server.Close)
	cachePath := filepath.Join(t.TempDir(), "commandcode-models.json")
	t.Setenv("COMMANDCODE_MODELS_URL", server.URL)
	t.Setenv("COMMANDCODE_MODELS_CACHE", cachePath)

	live := LoadCommandCodeModels(context.Background(), server.Client())
	if live.Source != CommandCodeModelsSourceLive || live.Warning != "" || len(live.Models) != 1 {
		t.Fatalf("unexpected live load: %+v", live)
	}
	// Updating an existing cache must work on Windows as well as Unix.
	live = LoadCommandCodeModels(context.Background(), server.Client())
	if live.Source != CommandCodeModelsSourceLive || live.Warning != "" {
		t.Fatalf("unexpected repeated live load: %+v", live)
	}

	body, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	var cache map[string]any
	if err := json.Unmarshal(body, &cache); err != nil {
		t.Fatalf("decode written cache: %v", err)
	}
	if commandCodeInt(cache["version"]) != commandCodeModelsCacheVersion {
		t.Fatalf("unexpected cache version: %+v", cache)
	}
	cachedEntries := commandCodeAnySlice(cache["models"])
	if len(cachedEntries) != 1 || commandCodeRecord(cachedEntries[0])["name"] != "Cached Model (CC)" {
		t.Fatalf("unexpected plugin-compatible cache: %+v", cache)
	}

	server.Close()
	cached := LoadCommandCodeModels(context.Background(), server.Client())
	if cached.Source != CommandCodeModelsSourceCache || len(cached.Models) != 1 || cached.Models[0].Name != "Cached Model (CC)" {
		t.Fatalf("unexpected cache fallback: %+v", cached)
	}
	if !strings.Contains(cached.Warning, "using the cached catalog") {
		t.Fatalf("expected cache fallback warning, got %q", cached.Warning)
	}
}

func TestLoadCommandCodeModelsReturnsEmptyWithoutValidCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "offline", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	t.Setenv("COMMANDCODE_MODELS_URL", server.URL)
	t.Setenv("COMMANDCODE_MODELS_CACHE", filepath.Join(t.TempDir(), "missing.json"))

	result := LoadCommandCodeModels(context.Background(), server.Client())
	if result.Source != CommandCodeModelsSourceEmpty || len(result.Models) != 0 {
		t.Fatalf("expected empty offline result, got %+v", result)
	}
	if !strings.Contains(result.Warning, "no valid cached catalog") || !strings.Contains(result.Warning, "next refresh succeeds") {
		t.Fatalf("unexpected empty-catalog warning: %q", result.Warning)
	}
}

func TestReadCommandCodeModelsCacheRejectsInvalidCatalogs(t *testing.T) {
	tests := map[string]string{
		"invalid json":      `not-json`,
		"wrong version":     `{"version":2,"models":[{"id":"model","name":"Model (CC)","reasoning":true,"contextWindow":1000,"maxTokens":1000}]}`,
		"empty models":      `{"version":1,"models":[]}`,
		"missing reasoning": `{"version":1,"models":[{"id":"model","name":"Model (CC)","contextWindow":1000,"maxTokens":1000}]}`,
		"invalid limits":    `{"version":1,"models":[{"id":"model","name":"Model (CC)","reasoning":true,"contextWindow":-1,"maxTokens":1000}]}`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "commandcode-models.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readCommandCodeModelsCache(path); err == nil {
				t.Fatal("expected invalid cache error")
			}
		})
	}
}

func TestLoadCommandCodeModelsKeepsLiveCatalogWhenCacheWriteFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"live-model","name":"Live Model","context_length":100000}]}`))
	}))
	defer server.Close()
	cachePath := filepath.Join(t.TempDir(), "cache-directory")
	if err := os.Mkdir(cachePath, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COMMANDCODE_MODELS_URL", server.URL)
	t.Setenv("COMMANDCODE_MODELS_CACHE", cachePath)

	result := LoadCommandCodeModels(context.Background(), server.Client())
	if result.Source != CommandCodeModelsSourceLive || len(result.Models) != 1 {
		t.Fatalf("expected usable live models, got %+v", result)
	}
	if !strings.Contains(result.Warning, "could not update") {
		t.Fatalf("expected cache write warning, got %q", result.Warning)
	}
}

func TestRefreshCommandCodeModelsClearsSnapshotWhenOfflineWithoutCache(t *testing.T) {
	isolateProviderRegistry(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "offline", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	t.Setenv("COMMANDCODE_MODELS_URL", server.URL)
	t.Setenv("COMMANDCODE_MODELS_CACHE", filepath.Join(t.TempDir(), "missing.json"))

	result, err := RefreshCommandCodeModelsWithResult(context.Background(), server.Client())
	if err != nil {
		t.Fatalf("empty catalog must remain a non-fatal provider load: %v", err)
	}
	if result.Source != CommandCodeModelsSourceEmpty || len(result.Models) != 0 || len(GetModels("commandcode")) != 0 {
		t.Fatalf("expected unavailable Command Code provider, got result=%+v registered=%+v", result, GetModels("commandcode"))
	}
}

func TestFetchCommandCodeModelsRejectsInvalidCatalog(t *testing.T) {
	tests := map[string]string{
		"wrong object": `{"object":"model","data":[]}`,
		"empty data":   `{"object":"list","data":[]}`,
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
