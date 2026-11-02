package pigo

import "errors"

// KiroIncrementalConfig has the explicit request/auth/schema fields of
// KiroBufferedConfig but selects live text and completed-only tool events.
// It is a distinct type: buffered callers never change behavior implicitly.
// Schemas are an exact raw catalog, not permission to execute declared tools.
type KiroIncrementalConfig KiroBufferedConfig

// KiroIncrementalOptions opts Auto into injected HTTP and/or explicit scoped
// sessions. Zero options reject Auto and nonempty SessionID. Keys are copied.
type KiroIncrementalOptions KiroLiveTextOptions

var errKiroIncrementalRegistration = errors.New("kiro: invalid explicit incremental configuration")

// RegisterKiroIncrementalModule installs only an explicitly selected API module.
// Text previews are provisional; tool lifecycle events require completed response
// validation. Execution still needs final success and Agent permission/schema/
// context checks. No network default, login, model catalog or retry is installed.
// Use Stream/StreamSimple guards, not raw GetAPIModule callbacks. Trusted sources,
// executor and body operations must cooperate with context. Previews cannot be
// recalled after a late failure. sourceID owns UnregisterAPIModules cleanup.
func RegisterKiroIncrementalModule(sourceID string, config KiroIncrementalConfig, options KiroIncrementalOptions) error {
	return registerKiroIncrementalModuleWithHistory(sourceID, config, options, kiroToolHistoryOriginal)
}

// RegisterKiroIncrementalModuleWithAuthoredToolHistory is an explicit alternative
// that re-encodes current argument objects as authored history, not original wire.
// It adds no persistence, tool execution authority, network default or retry.
// All other incremental delivery/permission/late-error boundaries are unchanged.
func RegisterKiroIncrementalModuleWithAuthoredToolHistory(sourceID string, config KiroIncrementalConfig, options KiroIncrementalOptions) error {
	return registerKiroIncrementalModuleWithHistory(sourceID, config, options, kiroToolHistoryAuthored)
}

func registerKiroIncrementalModuleWithHistory(sourceID string, config KiroIncrementalConfig, options KiroIncrementalOptions, history kiroToolHistoryPolicy) error {
	var affinity *KiroSessionAffinity
	if options.SessionAffinity != nil {
		input := options.SessionAffinity
		if !kiroSessionIDValid(input.Tenant) || len(input.Key) < 32 || len(input.Key) > 64 {
			return errKiroIncrementalRegistration
		}
		copied := *input
		copied.Key = append([]byte(nil), input.Key...)
		affinity = &copied
	}
	if err := registerKiroExplicitModuleWithHistory(sourceID, KiroBufferedConfig(config), affinity, options.AutoHTTP, kiroDeliveryIncremental, history); err != nil {
		return errKiroIncrementalRegistration
	}
	return nil
}
