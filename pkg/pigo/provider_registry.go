package pigo

import (
	"context"
	"fmt"
	"net/http"
	"sync"
)

type ProviderAuthResolveFunc func(Provider, AuthConfig, *http.Client, context.Context) (string, error)
type ProviderOptionsNormalizeFunc func(Model, ProviderStreamOptions) ProviderStreamOptions
type ProviderOptionsBuildFunc func(Model, SimpleStreamOptions) ProviderStreamOptions

type ProviderAuth struct {
	RequiresOAuth        bool
	EnvAPIKeyName        string
	ResolveAuthorization ProviderAuthResolveFunc
}

type ProviderCapabilities struct {
	SupportsStreaming          bool
	SupportsSession            bool
	SupportsPromptCacheKey     bool
	SupportsPromptCacheControl bool
	SupportsReasoningSummary   bool
	SupportsTextVerbosity      bool
	SupportsThinkingBudget     bool
	SupportsToolChoice         bool
	HostedTools                HostedToolCapabilities
}

type ProviderModule struct {
	Provider         Provider
	Models           map[string]Model
	Auth             ProviderAuth
	Capabilities     ProviderCapabilities
	BuildOptions     ProviderOptionsBuildFunc
	NormalizeOptions ProviderOptionsNormalizeFunc
}

type ProviderModuleFactory func() ProviderModule

type providerRegistryEntry struct {
	module  *ProviderModule
	factory ProviderModuleFactory
}

var (
	providerRegistryMu  sync.RWMutex
	providerRegistry    = map[Provider]*providerRegistryEntry{}
	providerResolveHook func(Provider)
)

func RegisterProviderModule(module ProviderModule) {
	registerProviderModule(module.Provider, &module, nil)
}

func RegisterLazyProviderModule(provider Provider, factory ProviderModuleFactory) {
	registerProviderModule(provider, nil, factory)
}

func registerProviderModule(provider Provider, module *ProviderModule, factory ProviderModuleFactory) {
	if provider == "" {
		panic("pigo: provider registration requires provider name")
	}
	if module == nil && factory == nil {
		panic("pigo: provider registration requires module or factory")
	}
	if module != nil {
		normalized := normalizeProviderModule(provider, *module)
		module = &normalized
	}

	providerRegistryMu.Lock()
	defer providerRegistryMu.Unlock()

	if _, exists := providerRegistry[provider]; exists {
		panic(fmt.Sprintf("pigo: provider %q already registered", provider))
	}

	providerRegistry[provider] = &providerRegistryEntry{
		module:  module,
		factory: factory,
	}
}

func resolveProviderModule(provider Provider) *ProviderModule {
	providerRegistryMu.RLock()
	entry := providerRegistry[provider]
	if entry == nil {
		providerRegistryMu.RUnlock()
		return nil
	}
	if entry.module != nil {
		module := entry.module
		providerRegistryMu.RUnlock()
		return module
	}
	factory := entry.factory
	providerRegistryMu.RUnlock()

	providerRegistryMu.Lock()
	defer providerRegistryMu.Unlock()

	entry = providerRegistry[provider]
	if entry == nil {
		return nil
	}
	if entry.module != nil {
		return entry.module
	}

	module := normalizeProviderModule(provider, factory())
	entry.module = &module
	if providerResolveHook != nil {
		providerResolveHook(provider)
	}
	return entry.module
}

func listRegisteredProviders() []Provider {
	providerRegistryMu.RLock()
	defer providerRegistryMu.RUnlock()

	providers := make([]Provider, 0, len(providerRegistry))
	for provider := range providerRegistry {
		providers = append(providers, provider)
	}
	return providers
}

func GetProviderCapabilities(provider Provider) ProviderCapabilities {
	module := resolveProviderModule(provider)
	if module == nil {
		return ProviderCapabilities{}
	}
	return module.Capabilities
}

func normalizeProviderModule(provider Provider, module ProviderModule) ProviderModule {
	if module.Provider == "" {
		module.Provider = provider
	}
	if module.Provider != provider {
		panic(fmt.Sprintf("pigo: provider module mismatch: registered=%q module=%q", provider, module.Provider))
	}
	if module.Models == nil {
		module.Models = map[string]Model{}
	}
	for modelID, model := range module.Models {
		if model.ID == "" {
			model.ID = modelID
		}
		model.Provider = provider
		module.Models[modelID] = model
	}
	return module
}
