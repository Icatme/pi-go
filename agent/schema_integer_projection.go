package agent

import (
	"encoding/json"
	"strconv"
	"strings"
)

// projectExactSchemaIntegers adapts json.Number's string representation to the
// validator's integer type classifier, without changing execution arguments.
// Nonintegers and values outside int64/uint64 retain their existing behavior.
func projectExactSchemaIntegers(value any) any {
	switch v := value.(type) {
	case json.Number:
		if n, integer := exactSchemaInteger(v); integer && n != nil {
			return n
		}
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			out[k] = projectExactSchemaIntegers(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = projectExactSchemaIntegers(item)
		}
		return out
	}
	return value
}

// exactSchemaInteger returns (nil, true) for an integer outside int64/uint64.
// It never expands more than 20 decimal digits, including exponent notation.
func exactSchemaInteger(value json.Number) (any, bool) {
	text := string(value)
	mantissa, expText := text, "0"
	if pos := strings.IndexAny(text, "eE"); pos >= 0 {
		mantissa, expText = text[:pos], text[pos+1:]
	}
	negative := strings.HasPrefix(mantissa, "-")
	if negative {
		mantissa = mantissa[1:]
	}
	fraction := 0
	if pos := strings.IndexByte(mantissa, '.'); pos >= 0 {
		fraction = len(mantissa) - pos - 1
		mantissa = mantissa[:pos] + mantissa[pos+1:]
	}
	if mantissa == "" {
		return nil, false
	}
	for _, r := range mantissa {
		if r < '0' || r > '9' {
			return nil, false
		}
	}
	mantissa = strings.TrimLeft(mantissa, "0")
	if mantissa == "" {
		return int64(0), true
	}
	exponent, err := strconv.Atoi(expText)
	if err != nil {
		return nil, !strings.HasPrefix(expText, "-")
	}
	if exponent > len(text)+20 {
		return nil, true
	}
	if exponent < -len(text)-20 {
		return nil, false
	}
	exponent -= fraction
	for strings.HasSuffix(mantissa, "0") {
		mantissa = mantissa[:len(mantissa)-1]
		exponent++
	}
	if exponent < 0 {
		return nil, false
	}
	if exponent > 20 || len(mantissa)+exponent > 20 {
		return nil, true
	}
	text = mantissa + strings.Repeat("0", exponent)
	if negative {
		text = "-" + text
	}
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		return n, true
	}
	if !negative {
		if n, err := strconv.ParseUint(text, 10, 64); err == nil {
			return n, true
		}
	}
	return nil, true
}
