package agent

import (
	"crypto/sha256"
	"encoding/json"
	"hash"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/Icatme/pi-go/internal/jsontext"
)

// toolProviderEvidence is immutable after construction. Ordinary ToolCall fmt
// output shows only its private pointer, not these fields; no logger is added.
// This is origin/change evidence, NOT JSON schema validity or execution approval.
type toolProviderEvidence struct {
	raw     string
	binding [32]byte
}

func (*toolProviderEvidence) String() string   { return "[provider evidence]" }
func (*toolProviderEvidence) GoString() string { return "[provider evidence]" }

func captureProviderRawArguments(raw string, call ToolCall) *toolProviderEvidence {
	if len(raw) < 2 || len(raw) > 1<<20 || jsontext.ValidateUnicode([]byte(raw)) != nil {
		return nil
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed[0] != '{' {
		return nil
	}
	binding, ok := providerToolBinding(call)
	if !ok {
		return nil
	}
	return &toolProviderEvidence{raw: raw, binding: binding}
}

func replayProviderRawArguments(call ToolCall) string {
	evidence := call.providerEvidence
	if evidence == nil {
		return ""
	}
	binding, ok := providerToolBinding(call)
	if !ok || binding != evidence.binding {
		return ""
	}
	return evidence.raw
}

// Typed hashing avoids invoking MarshalJSON, losing numeric literals or treating
// an edited representation as original wire. It does not reconstruct any raw JSON.
func providerToolBinding(call ToolCall) ([32]byte, bool) {
	zero := [32]byte{}
	if len(call.Arguments) > 1<<20 {
		return zero, false
	}
	h := sha256.New()
	for _, value := range []string{call.ID, call.OriginalID, call.Name, call.Namespace, call.ThoughtSignature} {
		if len(value) > 4096 {
			return zero, false
		}
		providerEvidenceWrite(h, "identity", value)
	}
	providerEvidenceWrite(h, "arguments", string(call.Arguments))
	nodes, bytesLeft := 4096, 1<<20
	if !providerEvidenceHashValue(h, call.ParsedArgs, 0, &nodes, &bytesLeft) {
		return zero, false
	}
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result, true
}

func providerEvidenceWrite(h hash.Hash, tag, value string) {
	h.Write([]byte(tag))
	h.Write([]byte(strconv.Itoa(len(value))))
	h.Write([]byte{':'})
	h.Write([]byte(value))
}

func providerEvidenceHashValue(h hash.Hash, value any, depth int, nodes, bytesLeft *int) bool {
	if depth > 64 || *nodes <= 0 {
		return false
	}
	*nodes--
	write := func(tag, s string) bool {
		if len(s) > *bytesLeft {
			return false
		}
		*bytesLeft -= len(s)
		providerEvidenceWrite(h, tag, s)
		return true
	}
	switch v := value.(type) {
	case nil:
		return write("nil", "")
	case bool:
		return write("bool", strconv.FormatBool(v))
	case string:
		return write("string", v)
	case json.Number:
		s := string(v)
		if len(s) == 0 || len(s) > 128 || !strings.ContainsRune("-0123456789", rune(s[0])) || !json.Valid([]byte(s)) {
			return false
		}
		return write("number", s)
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
		return write("float64", strconv.FormatUint(math.Float64bits(v), 16))
	case float32:
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return false
		}
		return write("float32", strconv.FormatUint(uint64(math.Float32bits(v)), 16))
	case int:
		return write("int", strconv.FormatInt(int64(v), 10))
	case int64:
		return write("int64", strconv.FormatInt(v, 10))
	case uint64:
		return write("uint64", strconv.FormatUint(v, 10))
	case map[string]any:
		if v == nil {
			return write("nil-map", "")
		}
		if len(v) > *nodes {
			return false
		}
		if !write("map", strconv.Itoa(len(v))) {
			return false
		}
		keys := make([]string, 0, len(v))
		for key := range v {
			if len(key) > *bytesLeft {
				return false
			}
			*bytesLeft -= len(key)
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			providerEvidenceWrite(h, "key", key)
			if !providerEvidenceHashValue(h, v[key], depth+1, nodes, bytesLeft) {
				return false
			}
		}
		return true
	case []any:
		if v == nil {
			return write("nil-array", "")
		}
		if len(v) > *nodes || !write("array", strconv.Itoa(len(v))) {
			return false
		}
		for _, item := range v {
			if !providerEvidenceHashValue(h, item, depth+1, nodes, bytesLeft) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
