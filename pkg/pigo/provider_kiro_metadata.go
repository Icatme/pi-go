package pigo

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strconv"
)

var errKiroMetadata = errors.New("kiro: invalid or conflicting response metadata")

// Metadata is reported evidence, not estimated usage. Credits/metering events
// are deliberately absent. Token counts retain int64 precision.
type kiroMetadataSnapshot struct {
	StopReason             string
	TokenCounts            map[string]int64
	ContextUsagePercentage *float64
	NormalizedTokenUsage   *float64
}

// This collector accepts classified metadataEvent JSON only. It is not wired
// to a provider. Fields merge without summation; a different repeat of the same
// field is rejected, not assumed to be a newer cumulative snapshot.
// A single goroutine owns the collector; result returns detached snapshots.
type kiroMetadataCollector struct {
	maxBytes      int
	maxEvents     int
	events        int
	terminal      error
	snapshot      kiroMetadataSnapshot
	fractionalRaw map[string]string
}

func newKiroMetadataCollector(maxBytes, maxEvents int) (*kiroMetadataCollector, error) {
	if maxBytes < 2 || maxEvents < 1 {
		return nil, errKiroMetadata
	}
	return &kiroMetadataCollector{maxBytes: maxBytes, maxEvents: maxEvents, snapshot: kiroMetadataSnapshot{TokenCounts: make(map[string]int64)}, fractionalRaw: make(map[string]string)}, nil
}

func (c *kiroMetadataCollector) accept(payload []byte) error {
	if c.terminal != nil {
		return c.terminal
	}
	if c.events >= c.maxEvents {
		return c.fail()
	}
	c.events++
	fields, err := decodeKiroJSONObject(payload, c.maxBytes)
	if err != nil {
		return c.fail()
	}
	for name, raw := range fields {
		switch name {
		case "stopReason":
			var reason string
			if json.Unmarshal(raw, &reason) != nil || !kiroMetadataStopKnown(reason) || (c.snapshot.StopReason != "" && c.snapshot.StopReason != reason) {
				return c.fail()
			}
			c.snapshot.StopReason = reason
		case "stopDetails":
			// Validate structure, but never retain or expose diagnostic bodies.
			if _, err := decodeKiroJSONObject(raw, c.maxBytes); err != nil {
				return c.fail()
			}
		case "tokenUsage":
			usage, err := decodeKiroJSONObject(raw, c.maxBytes)
			if err != nil {
				return c.fail()
			}
			for key, value := range usage {
				switch key {
				case "uncachedInputTokens", "outputTokens", "totalTokens", "cacheReadInputTokens", "cacheWriteInputTokens":
					count, ok := kiroMetadataCount(value)
					previous, exists := c.snapshot.TokenCounts[key]
					if !ok || (exists && previous != count) {
						return c.fail()
					}
					c.snapshot.TokenCounts[key] = count
				case "contextUsagePercentage", "normalizedTokenUsage":
					rawNumber := string(bytes.TrimSpace(value))
					if previous, exists := c.fractionalRaw[key]; exists && previous != rawNumber {
						return c.fail()
					}
					number, valid := kiroMetadataFraction(value, key == "contextUsagePercentage")
					if !valid {
						return c.fail()
					}
					slot := &c.snapshot.NormalizedTokenUsage
					if key == "contextUsagePercentage" {
						slot = &c.snapshot.ContextUsagePercentage
					}
					if *slot != nil && **slot != number {
						return c.fail()
					}
					*slot = &number
					c.fractionalRaw[key] = rawNumber
				default:
					return c.fail()
				}
			}
		default:
			return c.fail()
		}
	}
	return nil
}

func (c *kiroMetadataCollector) result() (kiroMetadataSnapshot, error) {
	if c.terminal != nil {
		return kiroMetadataSnapshot{}, c.terminal
	}
	copy := kiroMetadataSnapshot{StopReason: c.snapshot.StopReason, TokenCounts: make(map[string]int64)}
	for key, value := range c.snapshot.TokenCounts {
		copy.TokenCounts[key] = value
	}
	if c.snapshot.ContextUsagePercentage != nil {
		value := *c.snapshot.ContextUsagePercentage
		copy.ContextUsagePercentage = &value
	}
	if c.snapshot.NormalizedTokenUsage != nil {
		value := *c.snapshot.NormalizedTokenUsage
		copy.NormalizedTokenUsage = &value
	}
	return copy, nil
}

func (c *kiroMetadataCollector) fail() error {
	c.snapshot = kiroMetadataSnapshot{}
	c.fractionalRaw = nil
	c.terminal = errKiroMetadata
	return c.terminal
}

func kiroMetadataStopKnown(reason string) bool {
	switch reason {
	case "END_TURN", "MAX_TOKENS", "TOOL_USE", "CONTENT_FILTERED", "MODEL_CONTEXT_WINDOW_EXCEEDED", "PAUSE_TURN":
		return true
	default:
		return false
	}
}

// A deliberate strict subset: nonnegative base-10 integer tokens only; no
// fractional/exponent counts or overflow coercion. Zero is present, not missing.
func kiroMetadataCount(raw []byte) (int64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return 0, false
	}
	for _, digit := range raw {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	return value, err == nil
}

// Validate sign/range in decimal spelling BEFORE float conversion, so negative
// underflow cannot become allowed -0 and a value just above 100 cannot round
// down into range. Exponent notation is intentionally unsupported; no arbitrary
// precision allocation proportional to a wire-controlled exponent is needed.
func kiroMetadataFraction(raw []byte, percentage bool) (float64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return 0, false
	}
	dot := -1
	for i, digit := range raw {
		if digit == '.' && dot == -1 {
			dot = i
			continue
		}
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	integer := raw
	if dot >= 0 {
		if dot == 0 || dot == len(raw)-1 {
			return 0, false
		}
		integer = raw[:dot]
	}
	if percentage {
		if len(integer) > 3 || (len(integer) == 3 && bytes.Compare(integer, []byte("100")) > 0) {
			return 0, false
		}
		if bytes.Equal(integer, []byte("100")) && dot >= 0 {
			for _, digit := range raw[dot+1:] {
				if digit != '0' {
					return 0, false
				}
			}
		}
	}
	value, err := strconv.ParseFloat(string(raw), 64)
	return value, err == nil && !math.IsNaN(value) && !math.IsInf(value, 0)
}
