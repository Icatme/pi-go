package pigo

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

func TestLookupModelCapabilitiesUsesStaticRegistryFacts(t *testing.T) {
	snapshot, ok := LookupModelCapabilities("openai", "gpt-5.4")
	if !ok {
		t.Fatal("expected registered OpenAI model capability snapshot")
	}
	if snapshot.Provider != "openai" || snapshot.ModelID != "gpt-5.4" || snapshot.WireAPI != "openai-responses" {
		t.Fatalf("unexpected identity: %+v", snapshot)
	}
	if !slices.Equal(snapshot.Input, []InputType{InputText, InputImage}) || snapshot.ContextWindow != 272_000 || snapshot.MaxOutputTokens != 128_000 {
		t.Fatalf("unexpected model limits/input: %+v", snapshot)
	}
	assertCapability(t, "streaming", snapshot.Capabilities.Streaming, CapabilitySupported)
	assertCapability(t, "tools", snapshot.Capabilities.Tools, CapabilitySupported)
	assertCapability(t, "strict tools", snapshot.Capabilities.StrictTools, CapabilityUnsupported)
	assertCapability(t, "tool choice", snapshot.Capabilities.ToolChoice, CapabilitySupported)
	assertCapability(t, "temperature", snapshot.Capabilities.Temperature, CapabilitySupported)
	assertCapability(t, "top_p", snapshot.Capabilities.TopP, CapabilityUnsupported)
	assertCapability(t, "parallel tool calls", snapshot.Capabilities.ParallelToolCalls, CapabilityUnsupported)
	assertCapability(t, "reasoning", snapshot.Capabilities.Reasoning, CapabilitySupported)
	assertCapability(t, "reasoning levels", snapshot.Capabilities.ReasoningLevels, CapabilityUnknown)
	assertCapability(t, "json_object", snapshot.ResponseFormats.JSONObject, CapabilitySupported)
	assertCapability(t, "json_schema", snapshot.ResponseFormats.JSONSchema, CapabilitySupported)

	kimi, ok := LookupModelCapabilities("kimi-coding", "k2p5")
	if !ok {
		t.Fatal("expected registered Kimi model capability snapshot")
	}
	assertCapability(t, "Kimi tools", kimi.Capabilities.Tools, CapabilitySupported)
	assertCapability(t, "Kimi tool choice", kimi.Capabilities.ToolChoice, CapabilityUnsupported)
	if !slices.Equal(kimi.HostedTools, []HostedToolType{
		HostedToolTypeWebSearch,
		HostedToolTypeFetch,
		HostedToolTypeCodeRunner,
		HostedToolTypeExcel,
	}) {
		t.Fatalf("unexpected Kimi hosted tools: %+v", kimi.HostedTools)
	}
}

func TestLookupModelCapabilitiesDistinguishesWireAPIsAndExplicitReasoningLevels(t *testing.T) {
	luna, ok := LookupModelCapabilities("opencode-go", "gpt-5.6-luna")
	if !ok {
		t.Fatal("expected OpenCode Go Luna capability snapshot")
	}
	deepSeek, ok := LookupModelCapabilities("opencode-go", "deepseek-v4-flash")
	if !ok {
		t.Fatal("expected OpenCode Go DeepSeek capability snapshot")
	}
	miniMax, ok := LookupModelCapabilities("opencode-go", "minimax-m2.7")
	if !ok {
		t.Fatal("expected OpenCode Go MiniMax capability snapshot")
	}

	if luna.WireAPI != "openai-responses" || deepSeek.WireAPI != "openai-completions" || miniMax.WireAPI != "anthropic-messages" {
		t.Fatalf("unexpected wire APIs: luna=%q deepseek=%q minimax=%q", luna.WireAPI, deepSeek.WireAPI, miniMax.WireAPI)
	}
	if luna.ResponseFormats.JSONObject != CapabilitySupported || luna.ResponseFormats.JSONSchema != CapabilitySupported {
		t.Fatalf("expected Luna model-scoped structured output facts: %+v", luna.ResponseFormats)
	}
	if deepSeek.ResponseFormats.JSONObject != CapabilityUnsupported || deepSeek.ResponseFormats.JSONSchema != CapabilityUnsupported {
		t.Fatalf("expected Completions response formats to fail closed: %+v", deepSeek.ResponseFormats)
	}
	if miniMax.Capabilities.Temperature != CapabilityUnknown {
		t.Fatalf("expected conditional Anthropic temperature support to remain unknown, got %q", miniMax.Capabilities.Temperature)
	}

	wantLunaLevels := []ModelThinkingLevel{
		ModelThinkingLevelOff,
		ModelThinkingLevelLow,
		ModelThinkingLevelMedium,
		ModelThinkingLevelHigh,
		ModelThinkingLevelXHigh,
		ModelThinkingLevelMax,
	}
	if luna.Capabilities.ReasoningLevels != CapabilitySupported || !slices.Equal(luna.Capabilities.SupportedReasoningLevels, wantLunaLevels) {
		t.Fatalf("unexpected Luna reasoning levels: %+v", luna.Capabilities)
	}
	wantDeepSeekLevels := []ModelThinkingLevel{
		ModelThinkingLevelLow,
		ModelThinkingLevelHigh,
		ModelThinkingLevelMax,
	}
	if deepSeek.Capabilities.ReasoningLevels != CapabilitySupported || !slices.Equal(deepSeek.Capabilities.SupportedReasoningLevels, wantDeepSeekLevels) {
		t.Fatalf("unexpected DeepSeek reasoning levels: %+v", deepSeek.Capabilities)
	}
}

func TestDynamicCommandCodeCapabilitiesRemainUnknownWhenDiscoveryOmitsFacts(t *testing.T) {
	isolateProviderRegistry(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"live-only","name":"Live Only","context_length":100000}]}`))
	}))
	defer server.Close()
	t.Setenv("COMMANDCODE_MODELS_URL", server.URL)
	t.Setenv("COMMANDCODE_MODELS_CACHE", filepath.Join(t.TempDir(), "commandcode-models.json"))

	if _, err := RefreshCommandCodeModelsWithResult(context.Background(), server.Client()); err != nil {
		t.Fatalf("refresh Command Code models: %v", err)
	}
	snapshot, ok := LookupModelCapabilities("commandcode", "live-only")
	if !ok {
		t.Fatal("expected dynamically discovered model capability snapshot")
	}
	if snapshot.WireAPI != "commandcode-custom" {
		t.Fatalf("wire API = %q", snapshot.WireAPI)
	}
	assertCapability(t, "streaming", snapshot.Capabilities.Streaming, CapabilitySupported)
	assertCapability(t, "tools", snapshot.Capabilities.Tools, CapabilityUnknown)
	assertCapability(t, "reasoning", snapshot.Capabilities.Reasoning, CapabilityUnknown)
	assertCapability(t, "reasoning levels", snapshot.Capabilities.ReasoningLevels, CapabilityUnknown)
	assertCapability(t, "temperature", snapshot.Capabilities.Temperature, CapabilityUnsupported)
	assertCapability(t, "top_p", snapshot.Capabilities.TopP, CapabilityUnsupported)
	assertCapability(t, "parallel tool calls", snapshot.Capabilities.ParallelToolCalls, CapabilityUnsupported)
	if len(snapshot.Capabilities.SupportedReasoningLevels) != 0 {
		t.Fatalf("dynamic catalog must not invent reasoning levels: %+v", snapshot.Capabilities.SupportedReasoningLevels)
	}

	providerSnapshot, ok := SnapshotProviderModelCapabilities("commandcode")
	if !ok || len(providerSnapshot.Models) != 1 || providerSnapshot.Models[0].ModelID != "live-only" {
		t.Fatalf("unexpected provider snapshot: %+v", providerSnapshot)
	}
	if _, ok := LookupModelCapabilities("commandcode", "gpt-5.4"); ok {
		t.Fatal("replaced static catalog model must not remain queryable")
	}
}

func TestLookupModelCapabilitiesFailsClosedWithoutRegisteredFacts(t *testing.T) {
	isolateProviderRegistry(t)
	isolateAPIRegistry(t)

	if _, ok := LookupModelCapabilities("missing-provider", "missing-model"); ok {
		t.Fatal("unknown provider/model pair must not resolve")
	}

	provider := Provider("test-capability-provider")
	modelID := "gpt-5.6-name-must-not-infer"
	RegisterProviderModule(ProviderModule{
		Provider: provider,
		Capabilities: ProviderCapabilities{
			SupportsJSONOutput: true,
			SupportsJSONSchema: true,
		},
		Models: map[string]Model{
			modelID: {
				API:       "missing-wire-api",
				Reasoning: true,
				Capabilities: ModelCapabilities{
					Reasoning: CapabilityUnknown,
				},
			},
		},
	})

	snapshot, ok := LookupModelCapabilities(provider, modelID)
	if !ok {
		t.Fatal("registered model should resolve even when its wire API is unavailable")
	}
	assertCapability(t, "streaming", snapshot.Capabilities.Streaming, CapabilityUnsupported)
	assertCapability(t, "tools", snapshot.Capabilities.Tools, CapabilityUnsupported)
	assertCapability(t, "reasoning", snapshot.Capabilities.Reasoning, CapabilityUnsupported)
	assertCapability(t, "temperature", snapshot.Capabilities.Temperature, CapabilityUnsupported)
	assertCapability(t, "json_object", snapshot.ResponseFormats.JSONObject, CapabilityUnsupported)
	assertCapability(t, "json_schema", snapshot.ResponseFormats.JSONSchema, CapabilityUnsupported)
}

func TestLookupModelCapabilitiesMergesAPIProviderAndModelFacts(t *testing.T) {
	isolateProviderRegistry(t)
	isolateAPIRegistry(t)

	api := API("test-layered-capability-api")
	provider := Provider("test-layered-capability-provider")
	modelID := "gpt-5.6-name-must-not-infer"
	RegisterAPIModule(APIModule{
		API: api,
		Stream: func(Model, Context, ProviderStreamOptions) *AssistantMessageEventStream {
			return nil
		},
		Capabilities: ModelCapabilities{
			Tools:       CapabilitySupported,
			Temperature: CapabilitySupported,
		},
	})
	RegisterProviderModule(ProviderModule{
		Provider: provider,
		ModelCapabilities: ModelCapabilities{
			Temperature: CapabilityUnsupported,
		},
		Models: map[string]Model{
			modelID: {
				API:       api,
				Reasoning: true,
				Capabilities: ModelCapabilities{
					Tools:     CapabilityUnknown,
					Reasoning: CapabilityUnknown,
				},
			},
		},
	})

	snapshot, ok := LookupModelCapabilities(provider, modelID)
	if !ok {
		t.Fatal("expected layered capability snapshot")
	}
	assertCapability(t, "streaming", snapshot.Capabilities.Streaming, CapabilitySupported)
	assertCapability(t, "model tools override", snapshot.Capabilities.Tools, CapabilityUnknown)
	assertCapability(t, "provider temperature override", snapshot.Capabilities.Temperature, CapabilityUnsupported)
	assertCapability(t, "model reasoning override", snapshot.Capabilities.Reasoning, CapabilityUnknown)
	assertCapability(t, "unspecified top_p", snapshot.Capabilities.TopP, CapabilityUnknown)
}

func TestGetAPIModuleDoesNotExposeCapabilityMetadata(t *testing.T) {
	isolateAPIRegistry(t)

	api := API("test-cloned-capability-api")
	RegisterAPIModule(APIModule{
		API: api,
		Capabilities: ModelCapabilities{
			ReasoningLevels:          CapabilitySupported,
			SupportedReasoningLevels: []ModelThinkingLevel{ModelThinkingLevelLow},
		},
	})

	module := GetAPIModule(api)
	if module == nil {
		t.Fatal("expected registered API module")
	}
	module.Capabilities.SupportedReasoningLevels[0] = ModelThinkingLevelMax

	again := GetAPIModule(api)
	if again == nil || !slices.Equal(again.Capabilities.SupportedReasoningLevels, []ModelThinkingLevel{ModelThinkingLevelLow}) {
		t.Fatalf("API module capability mutation leaked into registry: %+v", again)
	}
}

func TestCapabilitySnapshotsAreDeepCopiedAndSorted(t *testing.T) {
	providerSnapshot, ok := SnapshotProviderModelCapabilities("opencode-go")
	if !ok || len(providerSnapshot.Models) < 2 {
		t.Fatalf("unexpected OpenCode Go snapshot: %+v", providerSnapshot)
	}
	if !slices.IsSortedFunc(providerSnapshot.Models, func(left, right ModelCapabilitySnapshot) int {
		if left.ModelID < right.ModelID {
			return -1
		}
		if left.ModelID > right.ModelID {
			return 1
		}
		return 0
	}) {
		t.Fatal("provider snapshot models must be sorted")
	}

	var luna *ModelCapabilitySnapshot
	for index := range providerSnapshot.Models {
		if providerSnapshot.Models[index].ModelID == "gpt-5.6-luna" {
			luna = &providerSnapshot.Models[index]
			break
		}
	}
	if luna == nil || len(luna.Input) == 0 || len(luna.Capabilities.SupportedReasoningLevels) == 0 {
		t.Fatalf("expected mutable snapshot copies for Luna: %+v", luna)
	}
	luna.Input[0] = "mutated"
	luna.Capabilities.SupportedReasoningLevels[0] = "mutated"

	again, ok := LookupModelCapabilities("opencode-go", "gpt-5.6-luna")
	if !ok {
		t.Fatal("expected Luna lookup after snapshot mutation")
	}
	if again.Input[0] == "mutated" || again.Capabilities.SupportedReasoningLevels[0] == "mutated" {
		t.Fatalf("snapshot mutation leaked into registry: %+v", again)
	}
}

func TestCapabilitySnapshotConcurrentWithCatalogReplacement(t *testing.T) {
	isolateProviderRegistry(t)

	provider := Provider("commandcode")
	replace := func(modelID string) error {
		module := normalizeProviderModule(provider, newCommandCodeProviderModuleWithModels(map[string]Model{
			modelID: newCommandCodeModel(modelID, modelID, true, 100_000, 65_536, UsageCost{}),
		}))
		if !providerRegistry.Replace(provider, &module) {
			return fmt.Errorf("provider %q not registered", provider)
		}
		return nil
	}
	if err := replace("dynamic-a"); err != nil {
		t.Fatal(err)
	}

	const iterations = 200
	errorsCh := make(chan error, 16)
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		for index := 0; index < iterations; index++ {
			modelID := "dynamic-a"
			if index%2 == 1 {
				modelID = "dynamic-b"
			}
			if err := replace(modelID); err != nil {
				errorsCh <- err
				return
			}
		}
	}()

	for reader := 0; reader < 8; reader++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := 0; index < iterations; index++ {
				snapshot, ok := SnapshotProviderModelCapabilities(provider)
				if !ok || len(snapshot.Models) != 1 {
					errorsCh <- fmt.Errorf("inconsistent provider snapshot: %+v", snapshot)
					return
				}
				model := snapshot.Models[0]
				if model.ModelID != "dynamic-a" && model.ModelID != "dynamic-b" {
					errorsCh <- fmt.Errorf("unexpected model snapshot: %+v", model)
					return
				}
				if model.WireAPI != "commandcode-custom" || model.Capabilities.Tools != CapabilityUnknown || model.Capabilities.Reasoning != CapabilityUnknown {
					errorsCh <- fmt.Errorf("inconsistent capability facts: %+v", model)
					return
				}
			}
		}()
	}

	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func assertCapability(t *testing.T, name string, got, want CapabilitySupport) {
	t.Helper()
	if got != want {
		t.Fatalf("%s capability = %q, want %q", name, got, want)
	}
}
