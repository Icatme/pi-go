package pigo

import (
	"fmt"
	"strings"
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
	module   *APIModule
	factory  APIModuleFactory
	sourceID string
}

var (
	apiRegistryMu  sync.RWMutex
	apiRegistry    = map[API]*apiRegistryEntry{}
	apiResolveHook func(API)
)

func RegisterAPIModule(module APIModule) {
	registerAPIModule(module.API, &module, nil, "")
}

func RegisterAPIModuleForSource(sourceID string, module APIModule) {
	registerAPIModule(module.API, &module, nil, normalizeAPIModuleSourceID(sourceID))
}

func RegisterLazyAPIModule(api API, factory APIModuleFactory) {
	registerAPIModule(api, nil, factory, "")
}

func RegisterLazyAPIModuleForSource(sourceID string, api API, factory APIModuleFactory) {
	registerAPIModule(api, nil, factory, normalizeAPIModuleSourceID(sourceID))
}

func registerAPIModule(api API, module *APIModule, factory APIModuleFactory, sourceID string) {
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
		module:   module,
		factory:  factory,
		sourceID: sourceID,
	}
}

func UnregisterAPIModules(sourceID string) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return
	}

	apiRegistryMu.Lock()
	defer apiRegistryMu.Unlock()

	for api, entry := range apiRegistry {
		if entry.sourceID == sourceID {
			delete(apiRegistry, api)
		}
	}
}

func normalizeAPIModuleSourceID(sourceID string) string {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		panic("pigo: api registration source id cannot be blank")
	}
	return sourceID
}

func ListAPIModules() []API {
	apiRegistryMu.RLock()
	defer apiRegistryMu.RUnlock()

	apis := make([]API, 0, len(apiRegistry))
	for api := range apiRegistry {
		apis = append(apis, api)
	}
	return apis
}

func GetAPIModule(api API) *APIModule {
	return resolveAPIModule(api)
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
