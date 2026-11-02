package pigo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"time"
)

// KiroCredentialScope is asserted by the caller, not verified account ownership.
type KiroCredentialScope = kiroCredentialScope

// KiroCredentialKind selects an explicit supported authentication header form.
type KiroCredentialKind = kiroCredentialKind

// KiroScopedCredential contains a secret. Its JSON/String forms redact Token,
// but the executor receives secret headers and must never log them.
type KiroScopedCredential = kiroScopedCredential

const (
	KiroCredentialBearer      = kiroCredentialBearer
	KiroCredentialAPIKey      = kiroCredentialAPIKey
	KiroCredentialExternalIDP = kiroCredentialExternalIDP
)

// KiroRequestIDs are supplied per attempt. The caller owns conversation affinity
// and invocation uniqueness; this package neither generates nor persists IDs.
type KiroRequestIDs struct{ ConversationID, InvocationID string }

// KiroBufferedConfig opts into a community-protocol, completed-response adapter.
// Callbacks are trusted, concurrency-safe and context-cooperative. No default
// network client, login, credentials, endpoint override, retry or tool execution.
// SystemPromptPolicy accepts "reject" (also the empty default) or the explicit
// semantic downgrade "prepend_first_user". Origin and UserAgent have no default.
type KiroBufferedConfig struct {
	API                                   API
	Provider                              Provider
	Region, ProfileARN, Origin, UserAgent string
	SystemPromptPolicy                    string
	Schemas                               map[string]json.RawMessage
	RequestIDs                            func(context.Context) (KiroRequestIDs, error)
	Credentials                           func(context.Context, KiroCredentialScope) (KiroScopedCredential, error)
	RoundTrip                             func(*http.Request) (*http.Response, error)
}

type kiroDeliveryMode uint8

const (
	kiroDeliveryBuffered kiroDeliveryMode = iota
	kiroDeliveryLiveText
	kiroDeliveryIncremental
)

var errKiroRegistration = errors.New("kiro: invalid explicit buffered configuration")

// RegisterKiroBufferedModule explicitly registers only an API module, not a model
// catalog or provider identity. The caller owns sourceID and unregisters using
// UnregisterAPIModules. Duplicate APIs panic like existing registration methods.
// Registration validates/copies config and never invokes callbacks. Requests
// return buffered lifecycle events only after complete response validation; this
// is NOT realtime streaming. This API does not establish service-use permission.
func RegisterKiroBufferedModule(sourceID string, config KiroBufferedConfig) error {
	return registerKiroBufferedModule(sourceID, config, nil, false)
}

func registerKiroBufferedModule(sourceID string, config KiroBufferedConfig, affinity *KiroSessionAffinity, autoHTTP bool) error {
	return registerKiroExplicitModule(sourceID, config, affinity, autoHTTP, kiroDeliveryBuffered)
}

func registerKiroExplicitModule(sourceID string, config KiroBufferedConfig, affinity *KiroSessionAffinity, autoHTTP bool, delivery kiroDeliveryMode) error {
	return registerKiroExplicitModuleWithHistory(sourceID, config, affinity, autoHTTP, delivery, kiroToolHistoryOriginal)
}

func registerKiroExplicitModuleWithHistory(sourceID string, config KiroBufferedConfig, affinity *KiroSessionAffinity, autoHTTP bool, delivery kiroDeliveryMode, history kiroToolHistoryPolicy) error {
	if history != kiroToolHistoryOriginal && history != kiroToolHistoryAuthored {
		return errKiroRegistration
	}
	if delivery > kiroDeliveryIncremental || strings.TrimSpace(sourceID) == "" || config.RequestIDs == nil || config.Credentials == nil || config.RoundTrip == nil || len(config.Schemas) > 128 || !kiroTextModelIdentityValid(Model{API: config.API, Provider: config.Provider, ID: "config-check"}) {
		return errKiroRegistration
	}
	catalog := make(map[string]json.RawMessage, len(config.Schemas))
	remaining := 1 << 20
	for name, raw := range config.Schemas {
		if kiroAllowedToolNames([]string{name}) == nil || len(raw) > remaining {
			return errKiroRegistration
		}
		if _, err := decodeKiroJSONObject(raw, remaining); err != nil {
			return errKiroRegistration
		}
		catalog[name] = append(json.RawMessage(nil), raw...)
		remaining -= len(raw)
	}
	config.Schemas = catalog
	// Offline constructor validates scope/client/policy without invoking a source.
	check := kiroBufferedAttemptConfig(config, Model{API: config.API, Provider: config.Provider, ID: "config-check"}, KiroRequestIDs{ConversationID: "config-check", InvocationID: "00000000-0000-0000-0000-000000000000"})
	request, err := newKiroOfflineHTTPRequest(context.Background(), Context{Messages: []Message{UserMessage{Content: "config-check"}}}, nil, check.Request)
	if err != nil {
		return errKiroRegistration
	}
	request.Body.Close()
	run := func(model Model, conversation Context, ctx context.Context, sessionID string) *AssistantMessageEventStream {
		fail := func() *AssistantMessageEventStream {
			if delivery == kiroDeliveryLiveText {
				return streamAPIUnavailable(model, "kiro: live text request rejected")
			}
			if delivery == kiroDeliveryIncremental {
				return streamAPIUnavailable(model, "kiro: incremental request rejected")
			}
			return streamAPIUnavailable(model, "kiro: buffered request rejected")
		}
		if ctx == nil || ctx.Err() != nil || model.API != config.API || model.Provider != config.Provider || !kiroTextModelIdentityValid(model) || model.BaseURL != "" || model.Compat != nil || len(model.Headers) != 0 || !reflect.ValueOf(model.SamplingParams).IsZero() || !reflect.ValueOf(model.SamplingParamsByThinkingLevel).IsZero() || !reflect.ValueOf(model.ThinkingLevelMap).IsZero() {
			return fail()
		}
		if delivery == kiroDeliveryLiveText && len(conversation.Tools) != 0 {
			return fail()
		}
		ids, err := config.RequestIDs(ctx)
		if err != nil || ctx.Err() != nil {
			return fail()
		}
		if affinity != nil {
			conversationID, valid := kiroSessionConversationID(config, *affinity, sessionID)
			if !valid || (ids.ConversationID != "" && ids.ConversationID != conversationID) {
				return fail()
			}
			ids.ConversationID = conversationID
		}
		// Identity callback runs before input preparation; credentials run only after
		// subsequent validation+serialization. Caller input stays read-only throughout.
		prepared, schemas, err := prepareKiroHostContext(conversation, catalog, 1<<20)
		if err != nil {
			return fail()
		}
		attempt := kiroBufferedAttemptConfig(config, model, ids)
		attempt.Request.ToolHistoryPolicy = history
		var stream *AssistantMessageEventStream
		if delivery == kiroDeliveryLiveText {
			live := kiroInjectedTextStreamConfig{Request: attempt.Request, Response: attempt.Response, Model: model, Timestamp: attempt.Timestamp, MaxTextBytes: attempt.MaxBytes, MaxEventBytes: attempt.MaxEventBytes}
			stream, err = startKiroScopedTextStream(ctx, prepared, live, config.Credentials, config.RoundTrip)
		} else if delivery == kiroDeliveryIncremental {
			stream, err = startKiroScopedMixedStream(ctx, prepared, schemas, attempt, config.Credentials, config.RoundTrip)
		} else {
			stream, err = runKiroScopedCompletedStream(ctx, prepared, schemas, attempt, config.Credentials, config.RoundTrip)
		}
		if err != nil {
			return fail()
		}
		return stream
	}
	module := APIModule{API: config.API, Capabilities: ModelCapabilities{CustomTools: CapabilitySupported},
		Stream: func(model Model, conversation Context, options ProviderStreamOptions) *AssistantMessageEventStream {
			return run(model, conversation, options.RequestContext, options.SessionID)
		},
		StreamSimple: func(model Model, conversation Context, options SimpleStreamOptions) *AssistantMessageEventStream {
			return run(model, conversation, options.RequestContext, options.SessionID)
		},
	}
	if delivery == kiroDeliveryLiveText {
		module.Capabilities.CustomTools = CapabilityUnsupported
	}
	validation := APIOptionValidation{
		ValidateOptions: func(model Model, options ProviderStreamOptions) error {
			if affinity != nil {
				if !kiroSessionIDValid(options.SessionID) {
					return errKiroHostOptions
				}
				options.SessionID = ""
			}
			if autoHTTP && options.Transport == TransportAuto {
				options.Transport = TransportSSE
			}
			return validateKiroHostOptions(model, options)
		},
		ValidateSimpleOptions: func(model Model, options SimpleStreamOptions) error {
			if affinity != nil {
				if !kiroSessionIDValid(options.SessionID) {
					return errKiroHostOptions
				}
				options.SessionID = ""
			}
			if autoHTTP && options.Transport == TransportAuto {
				options.Transport = TransportSSE
			}
			return validateKiroHostSimpleOptions(model, options)
		},
	}
	RegisterAPIModuleWithValidationForSource(sourceID, module, validation)
	return nil
}

func kiroBufferedAttemptConfig(config KiroBufferedConfig, model Model, ids KiroRequestIDs) kiroInjectedToolStreamConfig {
	return kiroInjectedToolStreamConfig{
		Request:  kiroHTTPRequestOptions{SystemPromptPolicy: kiroSystemPromptPolicy(config.SystemPromptPolicy), Region: config.Region, Identity: kiroRequestIdentity{ConversationID: ids.ConversationID, ProfileARN: config.ProfileARN, Origin: config.Origin, ModelID: model.ID}, Limits: kiroRequestLimits{Messages: 4096, Tools: 128, PayloadBytes: 1 << 20, WireBytes: 1 << 20}, UserAgent: config.UserAgent, InvocationID: ids.InvocationID},
		Response: kiroHTTPResponseLimits{WireBytes: 16 << 20, Stream: kiroOfflineLimits{MessageBytes: 1 << 20, TotalPayloadBytes: 4 << 20, TextBytes: 1 << 20, Events: 16384, Tools: 128, ArgumentBytes: 1 << 20}},
		Model:    model, Timestamp: time.Now().UTC(), MaxBytes: 1 << 20, MaxTools: 128, MaxEventBytes: assistantMessageEventByteBudget,
	}
}
