package pigo

import (
	"context"
	"fmt"
	"net/http"
)

type ProviderAuthResolveFunc func(Provider, AuthConfig, *http.Client, context.Context) (string, error)
type ProviderOptionsNormalizeFunc func(Model, ProviderStreamOptions) ProviderStreamOptions
type ProviderOptionsBuildFunc func(Model, SimpleStreamOptions) ProviderStreamOptions

type ProviderAuth struct {
	RequiresOAuth        bool
	EnvAPIKeyName        string
	EnvAPIKeyNames       []string
	ResolveAuthorization ProviderAuthResolveFunc
}

type ProviderCapabilities struct {
	SupportsStreaming          bool
	SupportsJSONOutput         bool
	SupportsJSONSchema         bool
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

var (
	providerRegistry = NewRegistry[Provider, ProviderModule]()
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
	if factory != nil {
		originalFactory := factory
		factory = func() ProviderModule {
			return normalizeProviderModule(provider, originalFactory())
		}
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			panic(fmt.Sprintf("pigo: provider %q already registered", provider))
		}
	}()
	providerRegistry.Register(provider, module, factory)
}

func resolveProviderModule(provider Provider) *ProviderModule {
	return providerRegistry.Resolve(provider)
}

func listRegisteredProviders() []Provider {
	return providerRegistry.Keys()
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
