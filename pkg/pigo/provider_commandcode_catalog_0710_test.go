package pigo

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// Expectations from pi-commandcode-provider 63c8e5c / CLI 1.88.0.
// Discovery supplies endpoint and context limits; metadata must not invent them.
func TestCommandCode0710DiscoveredMetadata(t *testing.T) {
	for _, tc := range []struct {
		id               string
		image, reasoning bool
		max              int
		effort           ModelThinkingLevel
		cost             UsageCost
	}{
		{"Qwen/Qwen3.8-Max-Prime", true, true, 131072, ModelThinkingLevelXHigh, UsageCost{Input: 4, Output: 12, CacheRead: .5, CacheWrite: 5}},
		{"inclusionai/ling-3.1-flash", false, true, 32768, ModelThinkingLevelHigh, UsageCost{Input: .3, Output: .9, CacheRead: .06}},
		{"meta/muse-spark-open", true, true, 65536, ModelThinkingLevelHigh, UsageCost{Input: 1.25, Output: 4.25, CacheRead: .15}},
		{"nvidia/nemotron-3.5-super-vl", true, true, 65536, ModelThinkingLevelHigh, UsageCost{Input: .2, Output: .5, CacheRead: .04}},
		{"minimax/minimax-m3.1-flash", true, true, 65536, ModelThinkingLevelMax, UsageCost{Input: .15, Output: .6, CacheRead: .015}},
		{"ornith/ornith-1.5-122b", true, true, 65536, ModelThinkingLevelOff, UsageCost{Input: .16, Output: .64, CacheRead: .02}},
		{"reflection/beam:free", false, true, 131072, ModelThinkingLevelMax, UsageCost{}},
		{"moonshotai/Kimi-K3.1-Pro", false, false, 65536, ModelThinkingLevelOff, UsageCost{Input: 2, Output: 10, CacheRead: .2}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			models := commandCodeModelsFromSpecs([]commandCodeModelSpec{{ID: tc.id, Name: tc.id, ContextWindow: 262144, API: "openai-completions"}}, true)
			model := models[tc.id]
			if model.Reasoning != tc.reasoning || slices.Contains(model.Input, InputImage) != tc.image || model.MaxTokens != tc.max || model.Cost != tc.cost {
				t.Fatalf("unexpected refreshed model: %+v", model)
			}
			if !slices.Contains(GetSupportedThinkingLevels(model), tc.effort) {
				t.Fatalf("missing effort %s", tc.effort)
			}
			if commandCodeNativeModel(model).API != "openai-completions" {
				t.Fatal("metadata overrode discovered protocol")
			}
			if model.Headers["x-command-code-version"] != "1.88.0" {
				t.Fatal("stale CLI snapshot version")
			}
			if _, enabled := newCommandCodeProviderModule().Models[tc.id]; enabled {
				t.Fatal("metadata silently enabled an undiscovered model")
			}
		})
	}
}

func TestCommandCode0710RetiredModelsNotAdvertised(t *testing.T) {
	for _, id := range []string{"inclusionai/ling-3.0-flash-sante:free", "inclusionai/ling-3.1-flash:free", "xiaomi/mimo-v2.5", "xiaomi/mimo-v2.5-pro", "stealth/cat-jam:free", "minimax/minimax-m3.1-flash:free", "ornith/ornith-1.5-122b:free"} {
		if _, ok := newCommandCodeProviderModule().Models[id]; ok {
			t.Errorf("retired model advertised: %s", id)
		}
		if _, ok := commandCodeModelCatalog[id]; ok {
			t.Errorf("retired capabilities retained: %s", id)
		}
		if _, ok := commandCodeModelCosts[id]; ok {
			t.Errorf("retired price retained: %s", id)
		}
	}
}

func TestCommandCode0710DiscoveryJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"object":"list","data":[{"id":"Qwen/Qwen3.8-Max-Prime","name":"Prime","context_length":8192,"supported_endpoints":["/chat/completions"]},{"id":"moonshotai/Kimi-K3.1-Pro","name":"Kimi","context_length":262144,"supported_endpoints":["/responses"]}]}`)
	}))
	defer server.Close()
	t.Setenv("COMMANDCODE_MODELS_URL", server.URL)
	models, err := FetchCommandCodeModels(context.Background(), server.Client())
	if err != nil || len(models) != 2 {
		t.Fatalf("discovery: %v models=%+v", err, models)
	}
	if models[0].MaxTokens != 8192 || !models[0].Reasoning || !slices.Contains(models[0].Input, InputImage) || commandCodeNativeModel(models[0]).API != "openai-completions" {
		t.Fatalf("new metadata changed discovered bounds or protocol: %+v", models[0])
	}
	if models[1].Reasoning || slices.Contains(models[1].Input, InputImage) || models[1].MaxTokens != 65536 || models[1].Capabilities.Reasoning != CapabilityUnknown || commandCodeNativeModel(models[1]).API != "openai-responses" {
		t.Fatalf("unknown capability model: %+v", models[1])
	}
}
