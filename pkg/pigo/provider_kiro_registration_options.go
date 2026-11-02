package pigo

const (
	kiroBufferedOptionAutoHTTP uint8 = iota + 1
	kiroBufferedOptionAffinity
)

// KiroBufferedOption is a controlled opt-in, not an arbitrary callback. Its zero
// value is invalid. Secret-bearing affinity options redact fmt and JSON fields.
type KiroBufferedOption struct {
	kind     uint8
	affinity *KiroSessionAffinity
}

func (KiroBufferedOption) String() string   { return "[kiro buffered option]" }
func (KiroBufferedOption) GoString() string { return "[kiro buffered option]" }

// WithKiroAutoHTTP opts the Auto selector into the sole implemented
// injected HTTP path. It adds no WebSocket attempt, retry/fallback or default
// network client. Explicit WebSocket remains rejected. Delivery is buffered.
func WithKiroAutoHTTP() KiroBufferedOption {
	return KiroBufferedOption{kind: kiroBufferedOptionAutoHTTP}
}

// WithKiroSessionAffinity snapshots caller-owned key material at option creation.
// Registration validates and copies it again. No key generation or persistence.
func WithKiroSessionAffinity(affinity KiroSessionAffinity) KiroBufferedOption {
	if !kiroSessionIDValid(affinity.Tenant) || len(affinity.Key) < 32 || len(affinity.Key) > 64 {
		return KiroBufferedOption{}
	}
	affinity.Key = append([]byte(nil), affinity.Key...)
	return KiroBufferedOption{kind: kiroBufferedOptionAffinity, affinity: &affinity}
}

// RegisterKiroBufferedModuleWithOptions leaves original registration APIs and
// public config layouts unchanged. At most one of each controlled option is
// accepted; zero/duplicate options reject before registration/callbacks.
// With no options this has the same strict behavior as RegisterKiroBufferedModule.
func RegisterKiroBufferedModuleWithOptions(sourceID string, config KiroBufferedConfig, options ...KiroBufferedOption) error {
	if len(options) > 2 {
		return errKiroRegistration
	}
	var affinity *KiroSessionAffinity
	autoHTTP := false
	for _, option := range options {
		switch option.kind {
		case kiroBufferedOptionAutoHTTP:
			if autoHTTP || option.affinity != nil {
				return errKiroRegistration
			}
			autoHTTP = true
		case kiroBufferedOptionAffinity:
			if affinity != nil || option.affinity == nil || !kiroSessionIDValid(option.affinity.Tenant) || len(option.affinity.Key) < 32 || len(option.affinity.Key) > 64 {
				return errKiroRegistration
			}
			copy := *option.affinity
			copy.Key = append([]byte(nil), copy.Key...)
			affinity = &copy
		default:
			return errKiroRegistration
		}
	}
	return registerKiroBufferedModule(sourceID, config, affinity, autoHTTP)
}
