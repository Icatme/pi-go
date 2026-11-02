package pigo

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"unicode"
	"unicode/utf8"
)

// KiroSessionAffinity is explicit caller-owned tenant/session isolation material.
// Key must contain 32..64 bytes of caller-managed secret entropy. It is copied at
// registration, not generated or persisted by this package. Key rotation changes
// all derived conversations. No memory zeroization guarantee is made.
type KiroSessionAffinity struct {
	Tenant string
	Key    []byte `json:"-"`
}

func (KiroSessionAffinity) String() string   { return "[kiro session affinity]" }
func (KiroSessionAffinity) GoString() string { return "[kiro session affinity]" }

// RegisterKiroSessionBufferedModule explicitly enables nonempty host SessionID.
// Conversation IDs bind tenant/routing/profile/region/session using HMAC-SHA256;
// no session map, randomness, logging or implicit conversation reset. RequestIDs
// must supply invocation identity and either an empty or exactly matching
// conversation ID; conflicts fail before credentials/HTTP. Default registration
// still rejects SessionID. Scope/tenant labels are assertions, not verified IAM.
func RegisterKiroSessionBufferedModule(sourceID string, config KiroBufferedConfig, affinity KiroSessionAffinity) error {
	if !kiroSessionIDValid(affinity.Tenant) || len(affinity.Key) < 32 || len(affinity.Key) > 64 {
		return errKiroRegistration
	}
	affinity.Key = append([]byte(nil), affinity.Key...)
	return registerKiroBufferedModule(sourceID, config, &affinity)
}
func kiroSessionIDValid(value string) bool {
	return value != "" && len(value) <= 256 && utf8.ValidString(value) && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}
func kiroSessionConversationID(config KiroBufferedConfig, affinity KiroSessionAffinity, sessionID string) (string, bool) {
	if !kiroSessionIDValid(sessionID) || !kiroSessionIDValid(affinity.Tenant) || len(affinity.Key) < 32 || len(affinity.Key) > 64 {
		return "", false
	}
	mac := hmac.New(sha256.New, affinity.Key)
	var length [8]byte
	for _, value := range []string{"pi-go/kiro-session/v1", affinity.Tenant, string(config.API), string(config.Provider), config.Region, config.ProfileARN, sessionID} {
		binary.BigEndian.PutUint64(length[:], uint64(len(value)))
		mac.Write(length[:])
		mac.Write([]byte(value))
	}
	return "pi-" + hex.EncodeToString(mac.Sum(nil)), true
}
