package pigo

import (
	"fmt"
	"strings"
)

type APIStreamFunc func(Model, Context, ProviderStreamOptions) *AssistantMessageEventStream
type APISimpleStreamFunc func(Model, Context, SimpleStreamOptions) *AssistantMessageEventStream

type APIModule struct {
	API          API
	Stream       APIStreamFunc
	StreamSimple APISimpleStreamFunc
}

type APIModuleFactory func() APIModule

var (
	apiRegistry          = NewRegistry[API, APIModule]()
	apiRegistrySourceIDs = map[API]string{}
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
	if factory != nil {
		originalFactory := factory
		factory = func() APIModule {
			return normalizeAPIModule(api, originalFactory())
		}
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			panic(fmt.Sprintf("pigo: api %q already registered", api))
		}
	}()
	apiRegistry.Register(api, module, factory)
	apiRegistrySourceIDs[api] = sourceID
}

func UnregisterAPIModules(sourceID string) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return
	}

	for api, registeredSourceID := range cloneAPIRegistrySourceIDs() {
		if registeredSourceID == sourceID {
			apiRegistry.Delete(api)
			delete(apiRegistrySourceIDs, api)
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
	return apiRegistry.Keys()
}

func GetAPIModule(api API) *APIModule {
	return resolveAPIModule(api)
}

func resolveAPIModule(api API) *APIModule {
	return apiRegistry.Resolve(api)
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

func cloneAPIRegistrySourceIDs() map[API]string {
	cloned := make(map[API]string, len(apiRegistrySourceIDs))
	for api, sourceID := range apiRegistrySourceIDs {
		cloned[api] = sourceID
	}
	return cloned
}
