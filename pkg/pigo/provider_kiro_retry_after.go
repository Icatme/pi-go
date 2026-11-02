package pigo

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

var errKiroRetryAfter = errors.New("kiro: unsupported or over-budget retry-after")

type kiroRetryAfterHint struct {
	Present bool
	Delay time.Duration
}

// parseKiroRetryAfter is a pure parser, never retry permission. Supply all raw
// values for the standard Retry-After header; duplicates fail, even if equal.
// The caller supplies the receipt time and maximum acceptable wait. A valid
// wait beyond that budget fails instead of being shortened. Only integer
// delay-seconds and canonical IMF-fixdate are supported in this strict subset.
func parseKiroRetryAfter(values []string, now time.Time, maxDelay time.Duration) (kiroRetryAfterHint, error) {
	zero := kiroRetryAfterHint{}
	if maxDelay < 0 || len(values) > 1 {
		return zero, errKiroRetryAfter
	}
	if len(values) == 0 {
		return zero, nil
	}
	if len(values[0]) > 128 {
		return zero, errKiroRetryAfter
	}
	value := strings.Trim(values[0], " \t")
	if value == "" {
		return zero, errKiroRetryAfter
	}
	digits := true
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			digits = false
			break
		}
	}
	if digits {
		seconds, err := strconv.ParseUint(value, 10, 64)
		// Compare before multiplying, avoiding duration overflow or rounding.
		if err != nil || seconds > uint64(maxDelay/time.Second) {
			return zero, errKiroRetryAfter
		}
		return kiroRetryAfterHint{Present: true, Delay: time.Duration(seconds) * time.Second}, nil
	}
	const layout = "Mon, 02 Jan 2006 15:04:05 GMT"
	if len(value) != len(layout) {
		return zero, errKiroRetryAfter
	}
	target, err := time.Parse(layout, value)
	// Roundtrip also rejects a mismatched weekday and noncanonical variants.
	if err != nil || target.Year() < 1900 || target.UTC().Format(layout) != value {
		return zero, errKiroRetryAfter
	}
	if !target.After(now) {
		return kiroRetryAfterHint{Present: true}, nil
	}
	// Compare timestamps before Sub: time.Duration subtraction saturates for
	// distant dates. A saturated result must not be mistaken for an exact hint.
	if target.After(now.Add(maxDelay)) {
		return zero, errKiroRetryAfter
	}
	return kiroRetryAfterHint{Present: true, Delay: target.Sub(now)}, nil
}
