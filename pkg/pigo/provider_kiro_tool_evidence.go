package pigo

import (
	"encoding/json"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// kiroToolArgumentEvidenceMatches permits the ordinary raw+parsed provider form
// only when both representations agree. Wire replay always uses original raw.
// It never calls a caller's MarshalJSON method or rebuilds raw from parsed data.
func kiroToolArgumentEvidenceMatches(raw string, parsed map[string]any, maxBytes int) bool {
	if _, err := decodeKiroJSONObject([]byte(raw), maxBytes); err != nil {
		return false
	}
	if parsed == nil {
		return true
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var evidence any
	if decoder.Decode(&evidence) != nil {
		return false
	}
	left := 4096
	return kiroToolEvidenceValueEqual(evidence, parsed, 0, &left)
}

func kiroToolEvidenceValueEqual(raw, parsed any, depth int, left *int) bool {
	if depth > 64 || *left <= 0 {
		return false
	}
	*left--
	switch value := raw.(type) {
	case nil:
		return parsed == nil
	case bool:
		other, ok := parsed.(bool)
		return ok && value == other
	case string:
		other, ok := parsed.(string)
		return ok && value == other
	case []any:
		other, ok := parsed.([]any)
		if !ok || other == nil || len(value) != len(other) {
			return false
		}
		for i := range value {
			if !kiroToolEvidenceValueEqual(value[i], other[i], depth+1, left) {
				return false
			}
		}
		return true
	case map[string]any:
		other, ok := parsed.(map[string]any)
		if !ok || other == nil || len(value) != len(other) {
			return false
		}
		for key, item := range value {
			matched, exists := other[key]
			if !exists || !kiroToolEvidenceValueEqual(item, matched, depth+1, left) {
				return false
			}
		}
		return true
	case json.Number:
		a, ok := kiroToolEvidenceNumber(string(value))
		if !ok {
			return false
		}
		var literal string
		switch n := parsed.(type) {
		case json.Number:
			literal = string(n)
		case float64:
			if math.IsNaN(n) || math.IsInf(n, 0) {
				return false
			}
			// Integer evidence must equal the actual binary float value, not its
			// shortest spelling (which can hide rounding at large magnitudes).
			if a.IsInt() {
				return a.Cmp(new(big.Rat).SetFloat64(n)) == 0
			}
			// Fractional decimals use explicit round-trip agreement, not a claim
			// that common decimals such as 0.1 have an exact binary encoding.
			literal = strconv.FormatFloat(n, 'g', -1, 64)
		case int:
			literal = strconv.FormatInt(int64(n), 10)
		case int64:
			literal = strconv.FormatInt(n, 10)
		case uint64:
			literal = strconv.FormatUint(n, 10)
		default:
			return false
		}
		b, ok := kiroToolEvidenceNumber(literal)
		return ok && a.Cmp(b) == 0
	default:
		return false
	}
}

func kiroToolEvidenceNumber(literal string) (*big.Rat, bool) {
	// Bound rational work independently of the caller's raw JSON byte budget. This is a
	// strict supported subset, not permission to round longer/exotic numbers.
	if len(literal) == 0 || len(literal) > 128 || strings.TrimSpace(literal) != literal || !json.Valid([]byte(literal)) {
		return nil, false
	}
	if index := strings.IndexAny(literal, "eE"); index >= 0 {
		exponent, err := strconv.ParseInt(literal[index+1:], 10, 32)
		if err != nil || exponent < -1024 || exponent > 1024 {
			return nil, false
		}
	}
	value, ok := new(big.Rat).SetString(literal)
	return value, ok
}
