package pigo

import (
	"encoding/json"
	"errors"
	"math"
	"unicode/utf8"
)

var errKiroAuthoredArguments = errors.New("kiro: unsupported caller-authored tool arguments")

// encodeKiroAuthoredToolArguments encodes CURRENT caller-owned values, not
// original provider wire evidence. It never fills ToolCall.RawArguments.
// Inputs must not be mutated concurrently. Only plain explicitly supported
// values are copied: no user MarshalJSON, String or reflection hooks run.
func encodeKiroAuthoredToolArguments(arguments map[string]any, maxBytes int) (json.RawMessage, error) {
	if arguments == nil || maxBytes < 2 || maxBytes > 1<<20 {
		return nil, errKiroAuthoredArguments
	}
	nodes, bytesLeft := 4096, maxBytes
	value, ok := copyKiroAuthoredArgument(arguments, 0, &nodes, &bytesLeft)
	if !ok {
		return nil, errKiroAuthoredArguments
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxBytes {
		return nil, errKiroAuthoredArguments
	}
	// Reuse the strict object/duplicate/Unicode/depth contract after encoding.
	if _, err := decodeKiroJSONObject(encoded, maxBytes); err != nil {
		return nil, errKiroAuthoredArguments
	}
	return encoded, nil
}

func copyKiroAuthoredArgument(value any, depth int, nodes, bytesLeft *int) (any, bool) {
	if depth > 64 || *nodes <= 0 {
		return nil, false
	}
	*nodes--
	consume := func(n int) bool {
		if n > *bytesLeft {
			return false
		}
		*bytesLeft -= n
		return true
	}
	switch v := value.(type) {
	case nil:
		return nil, consume(4)
	case bool:
		return v, consume(5)
	case string:
		return v, utf8.ValidString(v) && consume(len(v))
	case json.Number:
		if !consume(len(v)) {
			return nil, false
		}
		if _, ok := kiroToolEvidenceNumber(string(v)); !ok {
			return nil, false
		}
		return v, true
	case int:
		return v, consume(1)
	case int64:
		return v, consume(1)
	case uint64:
		return v, consume(1)
	case float64:
		// Above the safe-integer interval, a float can hide an already rounded
		// integer. Require json.Number/int64/uint64 instead; never repair it.
		if math.IsNaN(v) || math.IsInf(v, 0) || (math.Trunc(v) == v && math.Abs(v) > 9007199254740991) {
			return nil, false
		}
		return v, consume(1)
	case map[string]any:
		if v == nil || len(v) > *nodes {
			return nil, false
		}
		out := make(map[string]any, len(v))
		for key, item := range v {
			if !utf8.ValidString(key) || !consume(len(key)) {
				return nil, false
			}
			copied, ok := copyKiroAuthoredArgument(item, depth+1, nodes, bytesLeft)
			if !ok {
				return nil, false
			}
			out[key] = copied
		}
		return out, true
	case []any:
		if v == nil || len(v) > *nodes {
			return nil, false
		}
		out := make([]any, len(v))
		for i, item := range v {
			copied, ok := copyKiroAuthoredArgument(item, depth+1, nodes, bytesLeft)
			if !ok {
				return nil, false
			}
			out[i] = copied
		}
		return out, true
	case []string:
		if v == nil || len(v) > *nodes {
			return nil, false
		}
		out := make([]any, len(v))
		for i, item := range v {
			copied, ok := copyKiroAuthoredArgument(item, depth+1, nodes, bytesLeft)
			if !ok {
				return nil, false
			}
			out[i] = copied
		}
		return out, true
	default:
		return nil, false
	}
}
