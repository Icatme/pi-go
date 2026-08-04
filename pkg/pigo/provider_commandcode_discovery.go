package pigo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	commandCodeDefaultModelsURL      = "https://api.commandcode.ai/provider/v1/models"
	commandCodeModelResponseLimit    = 4 << 20
	commandCodeModelDiscoveryTimeout = 15 * time.Second
	commandCodeDefaultModelMaxTokens = 65_536
	commandCodeModelsCacheVersion    = 1
	commandCodeModelsCacheFileName   = "commandcode-models.json"
)

var commandCodeModelRefreshMu sync.Mutex

type commandCodeModelsResponse struct {
	Object string                `json:"object"`
	Data   []commandCodeAPIModel `json:"data"`
}

type commandCodeAPIModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextLength int    `json:"context_length"`
}

// CommandCodeModelsSource identifies where the current catalog came from.
type CommandCodeModelsSource string

const (
	CommandCodeModelsSourceLive  CommandCodeModelsSource = "live"
	CommandCodeModelsSourceCache CommandCodeModelsSource = "cache"
	CommandCodeModelsSourceEmpty CommandCodeModelsSource = "empty"
)

// CommandCodeModelsResult reports the catalog selected by the v0.4.3-compatible
// live/cache fallback. Warning is informational when Models remains usable.
type CommandCodeModelsResult struct {
	Models  []Model
	Source  CommandCodeModelsSource
	Warning string
}

type commandCodeModelsCache struct {
	Version int                      `json:"version"`
	Models  []commandCodeCachedModel `json:"models"`
}

type commandCodeCachedModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Reasoning     *bool  `json:"reasoning"`
	ContextWindow int    `json:"contextWindow"`
	MaxTokens     int    `json:"maxTokens"`
}

// FetchCommandCodeModels reads the current Provider API catalog without
// mutating the provider registry. COMMANDCODE_MODELS_URL and
// COMMANDCODE_API_BASE match pi-commandcode-provider's endpoint overrides.
func FetchCommandCodeModels(ctx context.Context, client *http.Client) ([]Model, error) {
	specs, err := fetchCommandCodeModelSpecs(ctx, client, resolveCommandCodeModelsURL())
	if err != nil {
		return nil, err
	}
	modelsByID := commandCodeModelsFromSpecs(specs, false)
	models := make([]Model, 0, len(specs))
	for _, spec := range specs {
		models = append(models, cloneModel(modelsByID[spec.ID]))
	}
	return models, nil
}

// LoadCommandCodeModels loads a live catalog and updates its cache. If live
// discovery fails, it uses the last valid v1 cache. A first offline load with
// no valid cache returns an empty catalog and a warning instead of failing.
func LoadCommandCodeModels(ctx context.Context, client *http.Client) CommandCodeModelsResult {
	cachePath, cachePathErr := resolveCommandCodeModelsCachePath()
	models, liveErr := FetchCommandCodeModels(ctx, client)
	if liveErr == nil {
		result := CommandCodeModelsResult{Models: models, Source: CommandCodeModelsSourceLive}
		if cachePathErr != nil {
			result.Warning = fmt.Sprintf("loaded the live Command Code model catalog but could not resolve its cache path: %v", cachePathErr)
			return result
		}
		if err := writeCommandCodeModelsCache(cachePath, models); err != nil {
			result.Warning = fmt.Sprintf("loaded the live Command Code model catalog but could not update %s: %v", cachePath, err)
		}
		return result
	}

	cacheErr := cachePathErr
	if cacheErr == nil {
		cachedModels, err := readCommandCodeModelsCache(cachePath)
		if err == nil {
			return CommandCodeModelsResult{
				Models:  cachedModels,
				Source:  CommandCodeModelsSourceCache,
				Warning: fmt.Sprintf("could not refresh the Command Code model catalog (%v); using the cached catalog from %s", liveErr, cachePath),
			}
		}
		cacheErr = err
	}

	cacheLocation := cachePath
	if cacheLocation == "" {
		cacheLocation = "the configured cache path"
	}
	return CommandCodeModelsResult{
		Models:  []Model{},
		Source:  CommandCodeModelsSourceEmpty,
		Warning: fmt.Sprintf("could not refresh the Command Code model catalog (%v), and no valid cached catalog is available at %s (%v); Command Code models will remain unavailable until the next refresh succeeds", liveErr, cacheLocation, cacheErr),
	}
}

// RefreshCommandCodeModelsWithResult applies the v0.4.3-compatible live/cache
// result to the provider registry and preserves non-fatal warning details.
func RefreshCommandCodeModelsWithResult(ctx context.Context, client *http.Client) (CommandCodeModelsResult, error) {
	commandCodeModelRefreshMu.Lock()
	defer commandCodeModelRefreshMu.Unlock()

	result := LoadCommandCodeModels(ctx, client)
	modelsByID := make(map[string]Model, len(result.Models))
	for _, model := range result.Models {
		modelsByID[model.ID] = cloneModel(model)
	}
	module := normalizeProviderModule("commandcode", newCommandCodeProviderModuleWithModels(modelsByID))
	if !providerRegistry.Replace(Provider("commandcode"), &module) {
		return CommandCodeModelsResult{}, errors.New("commandcode provider is not registered")
	}
	if refreshed := GetModels("commandcode"); len(refreshed) > 0 {
		result.Models = refreshed
	}
	return result, nil
}

// RefreshCommandCodeModels replaces the registered Command Code catalog. It
// accepts a valid cached catalog as a successful refresh and keeps the legacy
// error return for a first offline load with no usable catalog.
func RefreshCommandCodeModels(ctx context.Context, client *http.Client) ([]Model, error) {
	result, err := RefreshCommandCodeModelsWithResult(ctx, client)
	if err != nil {
		return nil, err
	}
	if result.Source == CommandCodeModelsSourceEmpty {
		return result.Models, errors.New(result.Warning)
	}
	return result.Models, nil
}

func fetchCommandCodeModelSpecs(ctx context.Context, client *http.Client, endpoint string) ([]commandCodeModelSpec, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, commandCodeModelDiscoveryTimeout)
		defer cancel()
	}
	if client == nil {
		client = http.DefaultClient
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create Command Code models request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch Command Code models: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 500))
		return nil, fmt.Errorf("fetch Command Code models: %s: %s", response.Status, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, commandCodeModelResponseLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read Command Code models: %w", err)
	}
	if len(body) > commandCodeModelResponseLimit {
		return nil, fmt.Errorf("decode Command Code models: response exceeds %d bytes", commandCodeModelResponseLimit)
	}
	var payload commandCodeModelsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode Command Code models: %w", err)
	}
	if payload.Object != "list" {
		return nil, fmt.Errorf("decode Command Code models: expected object %q, got %q", "list", payload.Object)
	}
	if payload.Data == nil {
		return nil, errors.New("decode Command Code models: expected data array")
	}
	if len(payload.Data) == 0 {
		return nil, errors.New("decode Command Code models: empty model catalog")
	}

	specs := make([]commandCodeModelSpec, 0, len(payload.Data))
	seen := make(map[string]struct{}, len(payload.Data))
	for index, model := range payload.Data {
		model.ID = strings.TrimSpace(model.ID)
		model.Name = strings.TrimSpace(model.Name)
		if model.ID == "" || model.Name == "" || model.ContextLength <= 0 {
			return nil, fmt.Errorf("decode Command Code models: invalid model at index %d", index)
		}
		if _, duplicate := seen[model.ID]; duplicate {
			return nil, fmt.Errorf("decode Command Code models: duplicate model %q", model.ID)
		}
		seen[model.ID] = struct{}{}
		specs = append(specs, commandCodeModelSpec{
			ID:            model.ID,
			Name:          model.Name,
			ContextWindow: model.ContextLength,
		})
	}
	return specs, nil
}

func readCommandCodeModelsCache(path string) ([]Model, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cache commandCodeModelsCache
	if err := json.Unmarshal(body, &cache); err != nil {
		return nil, fmt.Errorf("decode Command Code model cache: %w", err)
	}
	if cache.Version != commandCodeModelsCacheVersion {
		return nil, fmt.Errorf("decode Command Code model cache: expected version %d, got %d", commandCodeModelsCacheVersion, cache.Version)
	}
	if len(cache.Models) == 0 {
		return nil, errors.New("decode Command Code model cache: empty model catalog")
	}

	models := make([]Model, 0, len(cache.Models))
	seen := make(map[string]struct{}, len(cache.Models))
	for index, cached := range cache.Models {
		cached.ID = strings.TrimSpace(cached.ID)
		cached.Name = strings.TrimSpace(cached.Name)
		if cached.ID == "" || cached.Name == "" || cached.Reasoning == nil || cached.ContextWindow <= 0 || cached.MaxTokens <= 0 {
			return nil, fmt.Errorf("decode Command Code model cache: invalid model at index %d", index)
		}
		if _, duplicate := seen[cached.ID]; duplicate {
			return nil, fmt.Errorf("decode Command Code model cache: duplicate model %q", cached.ID)
		}
		seen[cached.ID] = struct{}{}
		models = append(models, newCommandCodeModel(
			cached.ID,
			cached.Name,
			*cached.Reasoning,
			cached.ContextWindow,
			cached.MaxTokens,
			commandCodeModelCosts[cached.ID],
		))
	}
	return models, nil
}

func writeCommandCodeModelsCache(path string, models []Model) error {
	cache := commandCodeModelsCache{
		Version: commandCodeModelsCacheVersion,
		Models:  make([]commandCodeCachedModel, 0, len(models)),
	}
	for _, model := range models {
		reasoning := model.Reasoning
		cache.Models = append(cache.Models, commandCodeCachedModel{
			ID:            model.ID,
			Name:          model.Name,
			Reasoning:     &reasoning,
			ContextWindow: model.ContextWindow,
			MaxTokens:     model.MaxTokens,
		})
	}
	body, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Command Code model cache: %w", err)
	}
	body = append(body, '\n')

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return nil
}

func resolveCommandCodeModelsURL() string {
	if endpoint := strings.TrimSpace(os.Getenv("COMMANDCODE_MODELS_URL")); endpoint != "" {
		return endpoint
	}
	return commandCodeDefaultModelsURL
}

func resolveCommandCodeModelsCachePath() (string, error) {
	if path := strings.TrimSpace(os.Getenv("COMMANDCODE_MODELS_CACHE")); path != "" {
		return path, nil
	}
	if agentDir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); agentDir != "" {
		return filepath.Join(agentDir, commandCodeModelsCacheFileName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".pi", "agent", commandCodeModelsCacheFileName), nil
}

func resolveCommandCodeAPIBaseURL() string {
	if endpoint := strings.TrimSpace(os.Getenv("COMMANDCODE_API_BASE")); endpoint != "" {
		return strings.TrimRight(endpoint, "/")
	}
	return commandCodeDefaultBaseURL
}
