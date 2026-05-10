package pigo

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func isolateProviderRegistry(t *testing.T) {
	t.Helper()

	previousRegistry := cloneProviderRegistryEntries(providerRegistry.snapshot(cloneProviderModulePointer))
	previousHook := cloneProviderResolveHook()
	providerRegistry.restore(cloneProviderRegistryEntries(previousRegistry))
	providerRegistry.SetResolveHook(nil)

	t.Cleanup(func() {
		providerRegistry.restore(previousRegistry)
		providerRegistry.SetResolveHook(previousHook)
	})
}

func cloneProviderRegistryEntries(entries map[Provider]*registryEntry[ProviderModule]) map[Provider]*registryEntry[ProviderModule] {
	cloned := make(map[Provider]*registryEntry[ProviderModule], len(entries))
	for provider, entry := range entries {
		if entry == nil {
			cloned[provider] = nil
			continue
		}
		entryCopy := *entry
		if entry.value != nil {
			moduleCopy := *entry.value
			if entry.value.Models != nil {
				moduleCopy.Models = make(map[string]Model, len(entry.value.Models))
				for modelID, model := range entry.value.Models {
					moduleCopy.Models[modelID] = cloneModel(model)
				}
			}
			entryCopy.value = &moduleCopy
		}
		cloned[provider] = &entryCopy
	}
	return cloned
}

func cloneProviderModulePointer(module *ProviderModule) *ProviderModule {
	if module == nil {
		return nil
	}
	cloned := *module
	if module.Models != nil {
		cloned.Models = make(map[string]Model, len(module.Models))
		for modelID, model := range module.Models {
			cloned.Models[modelID] = cloneModel(model)
		}
	}
	return &cloned
}

func cloneProviderResolveHook() func(Provider) {
	resolved := providerRegistry.resolveHook
	return resolved
}

func TestProviderRegistryConcurrentRegisterAndResolve(t *testing.T) {
	isolateProviderRegistry(t)

	const registrations = 32
	var registerWait sync.WaitGroup
	registerWait.Add(registrations)

	for index := 0; index < registrations; index++ {
		index := index
		go func() {
			defer registerWait.Done()
			provider := Provider(fmt.Sprintf("test-concurrent-provider-%d", index))
			RegisterProviderModule(ProviderModule{
				Provider: provider,
				Models: map[string]Model{
					"model": {ID: "model", API: "test-api", BaseURL: "https://example.invalid"},
				},
			})
		}()
	}
	registerWait.Wait()

	var resolveWait sync.WaitGroup
	resolveWait.Add(registrations)
	for index := 0; index < registrations; index++ {
		index := index
		go func() {
			defer resolveWait.Done()
			provider := Provider(fmt.Sprintf("test-concurrent-provider-%d", index))
			module := resolveProviderModule(provider)
			if module == nil {
				t.Errorf("expected provider %q to resolve", provider)
				return
			}
			if _, ok := module.Models["model"]; !ok {
				t.Errorf("expected provider %q to retain registered model", provider)
			}
		}()
	}
	resolveWait.Wait()
}

func TestProviderRegistryConcurrentLazyResolveLoadsFactoryOnce(t *testing.T) {
	isolateProviderRegistry(t)

	provider := Provider("test-lazy-concurrent-provider")
	beforeCount := len(listRegisteredProviders())
	var loadCount atomic.Int32
	RegisterLazyProviderModule(provider, func() ProviderModule {
		loadCount.Add(1)
		return ProviderModule{
			Provider: provider,
			Models: map[string]Model{
				"lazy": {ID: "lazy", API: "test-api", BaseURL: "https://example.invalid"},
			},
		}
	})

	var wait sync.WaitGroup
	for index := 0; index < 64; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			module := resolveProviderModule(provider)
			if module == nil || module.Models["lazy"].ID != "lazy" {
				t.Errorf("expected lazy provider module to resolve once")
			}
		}()
	}
	wait.Wait()

	if loadCount.Load() != 1 {
		t.Fatalf("expected lazy provider factory to run once, got %d", loadCount.Load())
	}
	if len(listRegisteredProviders()) != beforeCount+1 {
		t.Fatalf("expected resolve to preserve provider registration count, got %d", len(listRegisteredProviders()))
	}
}

func TestAPIRegistryConcurrentLazyResolveLoadsFactoryOnce(t *testing.T) {
	isolateAPIRegistry(t)

	api := API("test-lazy-concurrent-api")
	beforeCount := len(ListAPIModules())
	var loadCount atomic.Int32
	RegisterLazyAPIModule(api, func() APIModule {
		loadCount.Add(1)
		return APIModule{API: api}
	})

	var wait sync.WaitGroup
	for index := 0; index < 64; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			module := resolveAPIModule(api)
			if module == nil || module.API != api {
				t.Errorf("expected lazy api module to resolve")
			}
		}()
	}
	wait.Wait()

	if loadCount.Load() != 1 {
		t.Fatalf("expected lazy api factory to run once, got %d", loadCount.Load())
	}
	if len(ListAPIModules()) != beforeCount+1 {
		t.Fatalf("expected resolve to preserve api registration count, got %d", len(ListAPIModules()))
	}
}

func TestAPIRegistryConcurrentSourceAwareRegisterAndUnregister(t *testing.T) {
	isolateAPIRegistry(t)

	const registrations = 32
	var wait sync.WaitGroup

	for index := 0; index < registrations; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			sourceID := fmt.Sprintf("test-source-%d", index)
			api := API(fmt.Sprintf("test-source-aware-api-%d", index))

			RegisterAPIModuleForSource(sourceID, APIModule{API: api})
			if module := GetAPIModule(api); module == nil || module.API != api {
				t.Errorf("expected source-aware api %q to resolve after registration", api)
			}
			UnregisterAPIModules(sourceID)
		}()
	}
	wait.Wait()

	for index := 0; index < registrations; index++ {
		api := API(fmt.Sprintf("test-source-aware-api-%d", index))
		if module := GetAPIModule(api); module != nil {
			t.Fatalf("expected source-aware api %q to be removed after unregister", api)
		}
	}
}
