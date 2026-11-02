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
	Capabilities ModelCapabilities
}

// APIOptionValidation supplies optional pure/read-only preflight callbacks.
// Errors produce a fixed redacted failure. Validation is not execution permission.
type APIOptionValidation struct {
	ValidateOptions       func(Model, ProviderStreamOptions) error
	ValidateSimpleOptions func(Model, SimpleStreamOptions) error
}

// Keep public APIModule's layout unchanged. Module and validation are resolved
// as one generation so unregister/re-register cannot swap the guard.
type apiModuleRecord struct {
	module     APIModule
	validation APIOptionValidation
}

type APIModuleFactory func() APIModule

var (
	apiRegistry            = NewRegistry[API, apiModuleRecord]()
	apiRegistrySourceIDsMu sync.RWMutex
	apiRegistrySourceIDs   = map[API]string{}
)

func RegisterAPIModule(module APIModule) {
	registerAPIModule(module.API, &module, nil, "")
}

func RegisterAPIModuleForSource(sourceID string, module APIModule) {
	registerAPIModule(module.API, &module, nil, normalizeAPIModuleSourceID(sourceID))
}

// RegisterAPIModuleWithValidationForSource atomically registers a module and its
// option contract. Legacy registration remains unchanged; no API is enabled by
// merely constructing validation. In-flight snapshots survive unregistration.
func RegisterAPIModuleWithValidationForSource(sourceID string, module APIModule, validation APIOptionValidation) {
	registerAPIRecord(module.API, &module, nil, normalizeAPIModuleSourceID(sourceID), validation)
}

func RegisterLazyAPIModule(api API, factory APIModuleFactory) {
	registerAPIModule(api, nil, factory, "")
}

func RegisterLazyAPIModuleForSource(sourceID string, api API, factory APIModuleFactory) {
	registerAPIModule(api, nil, factory, normalizeAPIModuleSourceID(sourceID))
}

func registerAPIModule(api API, module *APIModule, factory APIModuleFactory, sourceID string) {
	registerAPIRecord(api, module, factory, sourceID, APIOptionValidation{})
}

func registerAPIRecord(api API, module *APIModule, factory APIModuleFactory, sourceID string, validation APIOptionValidation) {
	if api == "" {
		panic("pigo: api registration requires api name")
	}
	if module == nil && factory == nil {
		panic("pigo: api registration requires module or factory")
	}
	var record *apiModuleRecord
	if module != nil {
		record = &apiModuleRecord{module: normalizeAPIModule(api, *module), validation: validation}
	}
	var recordFactory func() apiModuleRecord
	if factory != nil {
		recordFactory = func() apiModuleRecord {
			return apiModuleRecord{module: normalizeAPIModule(api, factory()), validation: validation}
		}
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			panic(fmt.Sprintf("pigo: api %q already registered", api))
		}
	}()
	apiRegistrySourceIDsMu.Lock()
	defer apiRegistrySourceIDsMu.Unlock()
	apiRegistry.Register(api, record, recordFactory)
	apiRegistrySourceIDs[api] = sourceID
}

func UnregisterAPIModules(sourceID string) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return
	}

	apiRegistrySourceIDsMu.Lock()
	defer apiRegistrySourceIDsMu.Unlock()
	for api, registeredSourceID := range apiRegistrySourceIDs {
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
	module := resolveAPIModule(api)
	if module == nil {
		return nil
	}
	cloned := *module
	cloned.Capabilities = cloneModelCapabilities(module.Capabilities)
	return &cloned
}

func resolveAPIModule(api API) *APIModule {
	record := resolveAPIRecord(api)
	if record == nil {
		return nil
	}
	return &record.module
}
func resolveAPIRecord(api API) *apiModuleRecord {
	return apiRegistry.Resolve(api)
}

func normalizeAPIModule(api API, module APIModule) APIModule {
	if module.API == "" {
		module.API = api
	}
	if module.API != api {
		panic(fmt.Sprintf("pigo: api module mismatch: registered=%q module=%q", api, module.API))
	}
	module.Capabilities = cloneModelCapabilities(module.Capabilities)
	return module
}

func cloneAPIRegistrySourceIDs() map[API]string {
	apiRegistrySourceIDsMu.RLock()
	defer apiRegistrySourceIDsMu.RUnlock()

	cloned := make(map[API]string, len(apiRegistrySourceIDs))
	for api, sourceID := range apiRegistrySourceIDs {
		cloned[api] = sourceID
	}
	return cloned
}

func restoreAPIRegistrySourceIDs(sourceIDs map[API]string) {
	apiRegistrySourceIDsMu.Lock()
	defer apiRegistrySourceIDsMu.Unlock()

	cloned := make(map[API]string, len(sourceIDs))
	for api, sourceID := range sourceIDs {
		cloned[api] = sourceID
	}
	apiRegistrySourceIDs = cloned
}
