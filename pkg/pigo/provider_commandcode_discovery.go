package pigo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	commandCodeDefaultModelsURL      = "https://api.commandcode.ai/provider/v1/models"
	commandCodeModelResponseLimit    = 4 << 20
	commandCodeModelDiscoveryTimeout = 15 * time.Second
	commandCodeDefaultModelMaxTokens = 65_536
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

// FetchCommandCodeModels reads the current Provider API catalog without
// mutating the provider registry. COMMANDCODE_MODELS_URL and
// COMMANDCODE_API_BASE match pi-commandcode-provider v0.4.2's overrides.
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

// RefreshCommandCodeModels replaces the registered Command Code catalog with
// a validated Provider API snapshot. Existing readers keep their immutable
// module snapshot while new lookups see the refreshed models.
func RefreshCommandCodeModels(ctx context.Context, client *http.Client) ([]Model, error) {
	commandCodeModelRefreshMu.Lock()
	defer commandCodeModelRefreshMu.Unlock()

	models, err := FetchCommandCodeModels(ctx, client)
	if err != nil {
		return nil, err
	}
	modelsByID := make(map[string]Model, len(models))
	for _, model := range models {
		modelsByID[model.ID] = cloneModel(model)
	}
	module := normalizeProviderModule("commandcode", newCommandCodeProviderModuleWithModels(modelsByID))
	if !providerRegistry.Replace(Provider("commandcode"), &module) {
		return nil, errors.New("commandcode provider is not registered")
	}
	return GetModels("commandcode"), nil
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

func resolveCommandCodeModelsURL() string {
	if endpoint := strings.TrimSpace(os.Getenv("COMMANDCODE_MODELS_URL")); endpoint != "" {
		return endpoint
	}
	return commandCodeDefaultModelsURL
}

func resolveCommandCodeAPIBaseURL() string {
	if endpoint := strings.TrimSpace(os.Getenv("COMMANDCODE_API_BASE")); endpoint != "" {
		return strings.TrimRight(endpoint, "/")
	}
	return commandCodeDefaultBaseURL
}
