package pigo

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestKiroRetryAfterExactHints(t *testing.T) {
	now := time.Date(2026, time.November, 2, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		values []string
		budget time.Duration
		want kiroRetryAfterHint
	}{
		{nil, time.Minute, kiroRetryAfterHint{}},
		{[]string{"0"}, 0, kiroRetryAfterHint{Present: true}},
		{[]string{" \t0002\t "}, 2*time.Second, kiroRetryAfterHint{true, 2*time.Second}},
		{[]string{"Mon, 02 Nov 2026 00:00:02 GMT"}, 2*time.Second, kiroRetryAfterHint{true, 2*time.Second}},
		{[]string{"Mon, 02 Nov 2026 00:00:00 GMT"}, 0, kiroRetryAfterHint{Present: true}},
		{[]string{"Sun, 01 Nov 2026 23:59:59 GMT"}, 0, kiroRetryAfterHint{Present: true}},
	} {
		got, err := parseKiroRetryAfter(tc.values, now, tc.budget)
		if err != nil || got != tc.want { t.Fatalf("hint: %+v %v want %+v", got, err, tc.want) }
	}
	// Preserve a fractional remaining date wait; never round down.
	got, err := parseKiroRetryAfter([]string{"Mon, 02 Nov 2026 00:00:01 GMT"}, now.Add(500*time.Millisecond), time.Second)
	if err != nil || got.Delay != 500*time.Millisecond { t.Fatalf("fractional date: %+v %v", got, err) }
}

func TestKiroRetryAfterRejectsAmbiguousOrOverBudgetHints(t *testing.T) {
	now := time.Date(2026, time.November, 2, 0, 0, 0, 0, time.UTC)
	for _, values := range [][]string{
		{""}, {" "}, {"-1"}, {"+1"}, {"1.5"}, {"1e2"}, {"0x10"},
		{"NaN"}, {"Infinity"}, {"1, 2"}, {"1", "1"},
		{"1\r\n"}, {"\u00a01"}, {strings.Repeat("0",129)},
		{"18446744073709551616"}, {"9223372036854775807"},
		{"61"}, {"Mon, 02 Nov 2026 00:01:01 GMT"},
		{"Tue, 02 Nov 2026 00:00:01 GMT"},
		{"Mon, 01 Jan 0001 00:00:00 GMT"},
		{"Sat, 31 Dec 2016 23:59:60 GMT"},
		{"Mon, 02 Nov 2026 00:00:01 UTC"},
		{"Monday, 02-Nov-26 00:00:01 GMT"},
		{"Mon Nov  2 00:00:01 2026"},
		{"Fri, 31 Dec 9999 23:59:59 GMT"},
	} {
		got, err := parseKiroRetryAfter(values, now, time.Minute)
		if !errors.Is(err, errKiroRetryAfter) || !reflect.DeepEqual(got, kiroRetryAfterHint{}) {
			t.Fatalf("invalid hint accepted: %+v %v", got, err)
		}
	}
	if _, err := parseKiroRetryAfter(nil, now, -1); err == nil { t.Fatal("negative budget accepted") }
	// Nonintegral caller budget must not permit an integer wait above it.
	if _, err := parseKiroRetryAfter([]string{"1"}, now, 999*time.Millisecond); err == nil { t.Fatal("budget rounded up") }
}

func TestKiroRetryAfterDurationBoundary(t *testing.T) {
	now := time.Date(2026, time.November, 2, 0, 0, 0, 0, time.UTC)
	max := time.Duration(1<<63 - 1)
	got, err := parseKiroRetryAfter([]string{"9223372036"}, now, max)
	if err != nil || got.Delay != 9223372036*time.Second { t.Fatalf("exact boundary: %+v %v", got, err) }
	if _, err := parseKiroRetryAfter([]string{"9223372037"}, now, max); err == nil { t.Fatal("overflow accepted") }
	if _, err := parseKiroRetryAfter([]string{"Fri, 31 Dec 9999 23:59:59 GMT"}, now, max); err == nil { t.Fatal("saturated date difference accepted") }
}
