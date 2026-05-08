package pigo

import "testing"

func isolateAPIRegistry(t *testing.T) {
	t.Helper()

	apiRegistryMu.Lock()
	previousRegistry := cloneAPIRegistryEntries(apiRegistry)
	apiRegistry = cloneAPIRegistryEntries(previousRegistry)
	apiRegistryMu.Unlock()

	t.Cleanup(func() {
		apiRegistryMu.Lock()
		defer apiRegistryMu.Unlock()
		apiRegistry = previousRegistry
	})
}

func cloneAPIRegistryEntries(entries map[API]*apiRegistryEntry) map[API]*apiRegistryEntry {
	cloned := make(map[API]*apiRegistryEntry, len(entries))
	for api, entry := range entries {
		if entry == nil {
			cloned[api] = nil
			continue
		}
		copyEntry := *entry
		if entry.module != nil {
			moduleCopy := *entry.module
			copyEntry.module = &moduleCopy
		}
		cloned[api] = &copyEntry
	}
	return cloned
}

func TestUnregisterAPIModulesBySourceID(t *testing.T) {
	isolateAPIRegistry(t)

	sourceID := "test-source"
	api1 := API("test-api-1")
	api2 := API("test-api-2")

	registerAPIModule(api1, &APIModule{API: api1}, nil, sourceID)
	registerAPIModule(api2, &APIModule{API: api2}, nil, sourceID)

	apis := ListAPIModules()
	if len(apis) < 2 {
		t.Fatalf("expected at least 2 apis registered, got %d", len(apis))
	}

	UnregisterAPIModules(sourceID)

	apis = ListAPIModules()
	for _, api := range apis {
		if api == api1 || api == api2 {
			t.Fatalf("expected api %q to be unregistered", api)
		}
	}
}

func TestListAPIModulesReturnsRegisteredAPIs(t *testing.T) {
	isolateAPIRegistry(t)

	api := API("test-list-api")
	registerAPIModule(api, &APIModule{API: api}, nil, "")

	apis := ListAPIModules()
	found := false
	for _, a := range apis {
		if a == api {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected api %q in list", api)
	}
}

func TestRegisterLazyAPIModuleLoadsOnDemand(t *testing.T) {
	isolateAPIRegistry(t)

	api := API("test-lazy-api")
	loadCount := 0

	RegisterLazyAPIModule(api, func() APIModule {
		loadCount++
		return APIModule{
			Stream: func(model Model, ctx Context, options ProviderStreamOptions) *AssistantMessageEventStream {
				stream := newAssistantMessageEventStream()
				message := AssistantMessage{
					API:        model.API,
					Provider:   model.Provider,
					Model:      model.ID,
					StopReason: StopReasonStop,
					Content:    []ContentBlock{TextContent{Text: "lazy api"}},
				}
				stream.push(AssistantMessageEvent{Type: AssistantMessageEventStart, Partial: message})
				stream.push(AssistantMessageEvent{Type: AssistantMessageEventDone, Reason: message.StopReason, Message: message})
				stream.finish(message)
				return stream
			},
			StreamSimple: func(model Model, ctx Context, options SimpleStreamOptions) *AssistantMessageEventStream {
				stream := newAssistantMessageEventStream()
				message := AssistantMessage{
					API:        model.API,
					Provider:   model.Provider,
					Model:      model.ID,
					StopReason: StopReasonStop,
					Content:    []ContentBlock{TextContent{Text: "lazy api"}},
				}
				stream.push(AssistantMessageEvent{Type: AssistantMessageEventStart, Partial: message})
				stream.push(AssistantMessageEvent{Type: AssistantMessageEventDone, Reason: message.StopReason, Message: message})
				stream.finish(message)
				return stream
			},
		}
	})

	RegisterProviderModule(ProviderModule{
		Provider: "test-lazy-api-provider",
		Models: map[string]Model{
			"lazy-api-model": {
				ID:       "lazy-api-model",
				Name:     "Lazy API Model",
				API:      api,
				BaseURL:  "https://example.invalid",
				Provider: "test-lazy-api-provider",
			},
		},
	})

	if loadCount != 0 {
		t.Fatalf("expected lazy api module to remain unloaded before dispatch, got %d loads", loadCount)
	}

	model := GetModel("test-lazy-api-provider", "lazy-api-model")
	if model == nil {
		t.Fatal("expected lazy api model to exist")
	}

	result := CompleteSimple(*model, Context{}, SimpleStreamOptions{})
	if result.StopReason != StopReasonStop {
		t.Fatalf("expected lazy api dispatch to succeed, got %+v", result)
	}
	if loadCount != 1 {
		t.Fatalf("expected lazy api module to load exactly once, got %d", loadCount)
	}

	again := CompleteSimple(*model, Context{}, SimpleStreamOptions{})
	if again.StopReason != StopReasonStop {
		t.Fatalf("expected second lazy api dispatch to succeed, got %+v", again)
	}
	if loadCount != 1 {
		t.Fatalf("expected lazy api module to stay cached after first load, got %d", loadCount)
	}
}

func TestRegisterAPIModuleForSourceSupportsTargetedUnregister(t *testing.T) {
	isolateAPIRegistry(t)

	sourceID := "test-source-aware"
	targetedAPI := API("test-source-aware-api")
	staticAPI := API("test-static-api")

	RegisterAPIModuleForSource(sourceID, APIModule{API: targetedAPI})
	RegisterAPIModule(APIModule{API: staticAPI})

	UnregisterAPIModules(sourceID)

	if GetAPIModule(targetedAPI) != nil {
		t.Fatalf("expected api %q to be removed by source-aware unregister", targetedAPI)
	}
	if GetAPIModule(staticAPI) == nil {
		t.Fatalf("expected api %q registered without a source id to remain installed", staticAPI)
	}
}

func TestUnregisterAPIModulesIgnoresBlankSourceID(t *testing.T) {
	isolateAPIRegistry(t)

	staticAPI := API("test-blank-source-api")
	RegisterAPIModule(APIModule{API: staticAPI})

	if GetAPIModule("anthropic-messages") == nil {
		t.Fatal("expected built-in anthropic api to be registered")
	}

	UnregisterAPIModules("")

	if GetAPIModule(staticAPI) == nil {
		t.Fatalf("expected api %q to remain after blank source unregister", staticAPI)
	}
	if GetAPIModule("anthropic-messages") == nil {
		t.Fatal("expected blank source unregister to leave built-in apis installed")
	}
}
