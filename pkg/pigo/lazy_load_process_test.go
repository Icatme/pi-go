package pigo

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

type lazyLoadProbeResult struct {
	Providers []Provider `json:"providers"`
	APIs      []API      `json:"apis"`
}

func TestLazyLoadProcessImportOnlyDoesNotResolveModules(t *testing.T) {
	result := runLazyLoadProbe(t, "import_only")
	if len(result.Providers) != 0 || len(result.APIs) != 0 {
		t.Fatalf("expected import-only probe to resolve nothing, got %+v", result)
	}
}

func TestLazyLoadProcessGetModelResolvesProviderButNotAPI(t *testing.T) {
	result := runLazyLoadProbe(t, "get_model_openai_codex")
	if len(result.Providers) != 1 || result.Providers[0] != "openai-codex" {
		t.Fatalf("expected get_model probe to resolve only openai-codex provider, got %+v", result)
	}
	if len(result.APIs) != 0 {
		t.Fatalf("expected get_model probe to avoid api resolution, got %+v", result)
	}
}

func TestLazyLoadProcessCompleteSimpleResolvesProviderAndAPI(t *testing.T) {
	result := runLazyLoadProbe(t, "complete_simple_openai_codex")
	if len(result.Providers) != 1 || result.Providers[0] != "openai-codex" {
		t.Fatalf("expected complete probe to resolve openai-codex provider, got %+v", result)
	}
	if len(result.APIs) != 1 || result.APIs[0] != "openai-codex-responses" {
		t.Fatalf("expected complete probe to resolve codex api, got %+v", result)
	}
}

func TestLazyLoadProcessCompleteSimpleKimiResolvesProviderAndAPI(t *testing.T) {
	result := runLazyLoadProbe(t, "complete_simple_kimi")
	if len(result.Providers) != 1 || result.Providers[0] != "kimi-coding" {
		t.Fatalf("expected complete probe to resolve kimi provider, got %+v", result)
	}
	if len(result.APIs) != 1 || result.APIs[0] != "anthropic-messages" {
		t.Fatalf("expected complete probe to resolve anthropic-messages api, got %+v", result)
	}
}

func runLazyLoadProbe(t *testing.T, mode string) lazyLoadProbeResult {
	t.Helper()

	command := exec.Command("go", "test", "./pkg/pigo", "-run", "^TestLazyLoadProcessHelper$", "-count=1", "-v")
	command.Dir = "V:\\gitdownload\\pi-go"
	command.Env = append(os.Environ(),
		"PIGO_LAZY_LOAD_PROBE=1",
		"PIGO_LAZY_LOAD_MODE="+mode,
	)

	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("expected lazy load probe to succeed: %v\n%s", err, string(output))
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		jsonStart := strings.Index(line, "{")
		if jsonStart < 0 {
			continue
		}
		var result lazyLoadProbeResult
		if err := json.Unmarshal([]byte(line[jsonStart:]), &result); err != nil {
			t.Fatalf("expected valid probe json: %v\n%s", err, string(output))
		}
		return result
	}

	t.Fatalf("expected probe json in output, got:\n%s", string(output))
	return lazyLoadProbeResult{}
}

func TestLazyLoadProcessHelper(t *testing.T) {
	if os.Getenv("PIGO_LAZY_LOAD_PROBE") != "1" {
		t.Skip("lazy load process helper")
	}

	var (
		mu        sync.Mutex
		providers []Provider
		apis      []API
	)

	providerRegistry.SetResolveHook(func(provider Provider) {
		mu.Lock()
		defer mu.Unlock()
		providers = append(providers, provider)
	})
	apiRegistry.SetResolveHook(func(api API) {
		mu.Lock()
		defer mu.Unlock()
		apis = append(apis, api)
	})
	defer func() {
		providerRegistry.SetResolveHook(nil)
		apiRegistry.SetResolveHook(nil)
	}()

	switch os.Getenv("PIGO_LAZY_LOAD_MODE") {
	case "import_only":
	case "get_model_openai_codex":
		if GetModel("openai-codex", "gpt-5.4") == nil {
			t.Fatal("expected codex model during lazy load probe")
		}
	case "complete_simple_openai_codex":
		model := GetModel("openai-codex", "gpt-5.4")
		if model == nil {
			t.Fatal("expected codex model during lazy load probe")
		}
		_ = CompleteSimple(*model, Context{
			Messages: []Message{UserMessage{Content: "hi"}},
		}, SimpleStreamOptions{
			APIKey: makeOpenAICodexToken("acc_probe"),
		})
	case "complete_simple_kimi":
		model := GetModel("kimi-coding", "kimi-k2-thinking")
		if model == nil {
			t.Fatal("expected kimi model during lazy load probe")
		}
		_ = CompleteSimple(*model, Context{
			Messages: []Message{UserMessage{Content: "hi"}},
		}, SimpleStreamOptions{
			APIKey: "kimi-probe-key",
		})
	default:
		t.Fatalf("unknown lazy load probe mode %q", os.Getenv("PIGO_LAZY_LOAD_MODE"))
	}

	result := lazyLoadProbeResult{
		Providers: providers,
		APIs:      apis,
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("expected probe result to marshal: %v", err)
	}
	t.Log(string(payload))
}
