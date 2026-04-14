package pigo

import "testing"

func TestRegisterLazyAPIModuleLoadsOnDemand(t *testing.T) {
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
