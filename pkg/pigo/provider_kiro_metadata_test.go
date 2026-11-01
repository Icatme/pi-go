package pigo

import (
	"errors"
	"testing"
)

func kiroTestMetadata(t *testing.T) *kiroMetadataCollector {
	t.Helper()
	c, err := newKiroMetadataCollector(4096, 10)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestKiroMetadataPartialMergeAndPrecision(t *testing.T) {
	c := kiroTestMetadata(t)
	for _, input := range []string{
		`{"tokenUsage":{"uncachedInputTokens":9007199254740993,"outputTokens":0}}`,
		`{"tokenUsage":{"totalTokens":9007199254740999,"cacheReadInputTokens":6,"contextUsagePercentage":12.5}}`,
		`{"stopReason":"END_TURN","tokenUsage":{"normalizedTokenUsage":0.25}}`,
		`{"stopReason":"END_TURN","tokenUsage":{"outputTokens":0}}`,
	} {
		if err := c.accept([]byte(input)); err != nil {
			t.Fatal(err)
		}
	}
	s, err := c.result()
	if err != nil || s.TokenCounts["uncachedInputTokens"] != 9007199254740993 || s.TokenCounts["totalTokens"] != 9007199254740999 || s.StopReason != "END_TURN" || s.ContextUsagePercentage == nil || *s.ContextUsagePercentage != 12.5 {
		t.Fatalf("metadata corrupted: %#v, %v", s, err)
	}
	if value, present := s.TokenCounts["outputTokens"]; !present || value != 0 {
		t.Fatal("zero usage treated as missing")
	}
	s.TokenCounts["outputTokens"] = 100
	*s.ContextUsagePercentage = 100
	*s.NormalizedTokenUsage = 100
	again, _ := c.result()
	if again.TokenCounts["outputTokens"] != 0 || *again.ContextUsagePercentage != 12.5 || *again.NormalizedTokenUsage != 0.25 {
		t.Fatal("snapshot aliases collector state")
	}
}

func TestKiroMetadataConflictsAreTerminal(t *testing.T) {
	for _, pair := range [][2]string{
		{`{"stopReason":"CONTENT_FILTERED"}`, `{"stopReason":"END_TURN"}`},
		{`{"stopReason":"END_TURN"}`, `{"stopReason":"TOOL_USE"}`},
		{`{"tokenUsage":{"outputTokens":1}}`, `{"tokenUsage":{"outputTokens":2}}`},
		{`{"tokenUsage":{"contextUsagePercentage":50}}`, `{"tokenUsage":{"contextUsagePercentage":60}}`},
		{`{"tokenUsage":{"contextUsagePercentage":50}}`, `{"tokenUsage":{"contextUsagePercentage":50.0}}`},
		{`{"tokenUsage":{"normalizedTokenUsage":0.1}}`, `{"tokenUsage":{"normalizedTokenUsage":0.10000000000000001}}`},
	} {
		c := kiroTestMetadata(t)
		if err := c.accept([]byte(pair[0])); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(c.accept([]byte(pair[1])), errKiroMetadata) || !errors.Is(c.accept([]byte(`{}`)), errKiroMetadata) {
			t.Fatal("conflict did not permanently fail")
		}
		s, err := c.result()
		if err == nil || s.StopReason != "" || s.TokenCounts != nil {
			t.Fatal("failed collector exposed previous success evidence")
		}
	}
}

func TestKiroMetadataRollbackAndIgnoredStopDetails(t *testing.T) {
	c := kiroTestMetadata(t)
	if err := c.accept([]byte(`{"stopReason":"END_TURN","tokenUsage":{"outputTokens":9223372036854775807},"stopDetails":{"fixture":"first"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := c.accept([]byte(`{"stopDetails":{"fixture":"changed and discarded"}}`)); err != nil {
		t.Fatal("valid ignored details should not conflict", err)
	}
	s, err := c.result()
	if err != nil || s.TokenCounts["outputTokens"] != 9223372036854775807 {
		t.Fatal("MaxInt64 was not preserved")
	}
	// The success state must disappear regardless of map iteration order in
	// a subsequent event mixing valid fields and an unsupported field.
	if err := c.accept([]byte(`{"stopReason":"END_TURN","tokenUsage":{"cacheReadInputTokens":1},"unknown":true}`)); err == nil {
		t.Fatal("unknown field accepted")
	}
	s, err = c.result()
	if err == nil || s.StopReason != "" || s.TokenCounts != nil || s.NormalizedTokenUsage != nil || s.ContextUsagePercentage != nil {
		t.Fatal("failed event did not clear all evidence")
	}
}

func TestKiroMetadataLongFractionPolicy(t *testing.T) {
	c := kiroTestMetadata(t)
	// Ancillary values are float64: small positive decimal values may round
	// down to zero. Range sign checks are lexical; exponent notation is rejected.
	input := `{"tokenUsage":{"normalizedTokenUsage":0.`
	for i := 0; i < 400; i++ {
		input += "0"
	}
	input += `1}}`
	if err := c.accept([]byte(input)); err != nil {
		t.Fatal(err)
	}
	s, err := c.result()
	if err != nil || s.NormalizedTokenUsage == nil || *s.NormalizedTokenUsage != 0 {
		t.Fatal("documented ancillary underflow policy changed")
	}
	c, _ = newKiroMetadataCollector(64, 1)
	if c.accept([]byte(input)) == nil {
		t.Fatal("long fraction bypassed byte budget")
	}
}

func FuzzKiroMetadataTerminalState(f *testing.F) {
	f.Add([]byte(`{"stopReason":"END_TURN"}`), []byte(`{"stopReason":"CONTENT_FILTERED"}`))
	f.Add([]byte(`{"tokenUsage":{"outputTokens":0}}`), []byte(`{}`))
	f.Fuzz(func(t *testing.T, first, second []byte) {
		c, err := newKiroMetadataCollector(8192, 2)
		if err != nil {
			t.Fatal(err)
		}
		err1 := c.accept(first)
		err2 := c.accept(second)
		if err1 != nil && err2 != err1 {
			t.Fatal("terminal metadata error reset")
		}
		s, resultErr := c.result()
		if err2 != nil && (resultErr == nil || s.StopReason != "" || s.TokenCounts != nil) {
			t.Fatal("failed collector returned usable evidence")
		}
	})
}

func TestKiroMetadataRejectsInvalidAndMetering(t *testing.T) {
	for _, input := range []string{
		`{"stopReason":null}`, `{"stopReason":"end_turn"}`, `{"stopReason":"future"}`,
		`{"stopReason":"END_TURN","stopReason":"TOOL_USE"}`,
		`{"stopDetails":[]}`, `{"tokenUsage":null}`, `{"usage":0.25,"unit":"credit"}`,
		`{"tokenUsage":{"outputTokens":-1}}`, `{"tokenUsage":{"outputTokens":1.5}}`,
		`{"tokenUsage":{"outputTokens":1e3}}`, `{"tokenUsage":{"outputTokens":9223372036854775808}}`,
		`{"tokenUsage":{"outputTokens":"1"}}`, `{"tokenUsage":{"outputTokens":null}}`,
		`{"tokenUsage":{"contextUsagePercentage":101}}`, `{"tokenUsage":{"contextUsagePercentage":null}}`,
		`{"tokenUsage":{"contextUsagePercentage":100.00000000000000001}}`,
		`{"tokenUsage":{"normalizedTokenUsage":-1e-999}}`, `{"tokenUsage":{"contextUsagePercentage":-1e-999}}`,
		`{"tokenUsage":{"normalizedTokenUsage":-0}}`, `{"tokenUsage":{"contextUsagePercentage":1e2}}`,
		`{"tokenUsage":{"normalizedTokenUsage":-0.5}}`, `{"tokenUsage":{"normalizedTokenUsage":1e999}}`,
		`{"tokenUsage":{"credits":0.25}}`, `{"unknown":true}`,
	} {
		c := kiroTestMetadata(t)
		if c.accept([]byte(input)) == nil {
			t.Errorf("accepted unsupported metadata: %s", input)
		}
	}
}

func TestKiroMetadataExactPercentageBoundary(t *testing.T) {
	for _, input := range []string{`{"tokenUsage":{"contextUsagePercentage":100.00000000000000000}}`, `{"tokenUsage":{"contextUsagePercentage":0,"normalizedTokenUsage":0}}`} {
		c := kiroTestMetadata(t)
		if err := c.accept([]byte(input)); err != nil {
			t.Fatalf("valid boundary rejected: %s: %v", input, err)
		}
	}
}

func TestKiroMetadataBudgets(t *testing.T) {
	c, _ := newKiroMetadataCollector(2, 1)
	if c.accept([]byte(`{}`)) != nil || c.accept([]byte(`{}`)) == nil {
		t.Fatal("event count budget ignored")
	}
	c, _ = newKiroMetadataCollector(2, 10)
	if c.accept([]byte(`{"stopReason":"END_TURN"}`)) == nil {
		t.Fatal("event byte budget ignored")
	}
	if _, err := newKiroMetadataCollector(1, 1); err == nil {
		t.Fatal("invalid byte budget accepted")
	}
	if _, err := newKiroMetadataCollector(2, 0); err == nil {
		t.Fatal("invalid event budget accepted")
	}
}
