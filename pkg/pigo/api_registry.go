package pigo

import (
	"fmt"
	"sync"
)

type APIStreamFunc func(Model, Context, ProviderStreamOptions) *AssistantMessageEventStream
type APISimpleStreamFunc func(Model, Context, SimpleStreamOptions) *AssistantMessageEventStream

type APIModule struct {
	API          API
	Stream       APIStreamFunc
	StreamSimple APISimpleStreamFunc
}

type APIModuleFactory func() APIModule

type apiRegistryEntry struct {
	module  *APIModule
	factory APIModuleFactory
}

var (
	apiRegistryMu sync.RWMutex
	apiRegistry   = map[API]*apiRegistryEntry{}
	apiResolveHook func(API)
)

func RegisterAPIModule(module APIModule) {
	registerAPIModule(module.API, &module, nil)
}

func RegisterLazyAPIModule(api API, factory APIModuleFactory) {
	registerAPIModule(api, nil, factory)
}

func registerAPIModule(api API, module *APIModule, factory APIModuleFactory) {
	if api == "" {
		panic("pigo: api registration requires api name")
	}
	if module == nil && factory == nil {
		panic("pigo: api registration requires module or factory")
	}
	if module != nil {
		normalized := normalizeAPIModule(api, *module)
		module = &normalized
	}

	apiRegistryMu.Lock()
	defer apiRegistryMu.Unlock()

	if _, exists := apiRegistry[api]; exists {
		panic(fmt.Sprintf("pigo: api %q already registered", api))
	}

	apiRegistry[api] = &apiRegistryEntry{
		module:  module,
		factory: factory,
	}
}

func resolveAPIModule(api API) *APIModule {
	apiRegistryMu.RLock()
	entry := apiRegistry[api]
	if entry == nil {
		apiRegistryMu.RUnlock()
		return nil
	}
	if entry.module != nil {
		module := entry.module
		apiRegistryMu.RUnlock()
		return module
	}
	factory := entry.factory
	apiRegistryMu.RUnlock()

	apiRegistryMu.Lock()
	defer apiRegistryMu.Unlock()

	entry = apiRegistry[api]
	if entry == nil {
		return nil
	}
	if entry.module != nil {
		return entry.module
	}

	module := normalizeAPIModule(api, factory())
	entry.module = &module
	if apiResolveHook != nil {
		apiResolveHook(api)
	}
	return entry.module
}

func normalizeAPIModule(api API, module APIModule) APIModule {
	if module.API == "" {
		module.API = api
	}
	if module.API != api {
		panic(fmt.Sprintf("pigo: api module mismatch: registered=%q module=%q", api, module.API))
	}
	return module
}
