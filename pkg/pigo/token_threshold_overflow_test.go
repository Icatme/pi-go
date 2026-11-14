package pigo

import (
	"math/big"
	"testing"
)

func TestCostTierDoesNotWrapInputTokenSum(t *testing.T) {
	max := int(^uint(0) >> 1)
	model := Model{Cost: UsageCost{Output: 1}, CostTiers: []ModelCostTier{
		{InputTokensAbove: max - 2, Rates: UsageCost{Output: 3}},
		{InputTokensAbove: 100, Rates: UsageCost{Output: 2}},
	}}
	for _, tc := range []struct {
		name            string
		in, read, write int
		want            float64
	}{
		{"at lower", 80, 10, 10, 1}, {"above lower", 81, 10, 10, 2},
		{"at upper", max - 4, 1, 1, 2}, {"above upper", max - 3, 1, 1, 3},
		{"sum beyond int", max, 1, 1, 3}, {"all large", max, max, max, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := Usage{Input: tc.in, CacheRead: tc.read, CacheWrite: tc.write, Output: 1000000}
			c := CalculateCost(model, u)
			if c.Output != tc.want {
				t.Fatalf("cost=%+v want output=%v", c, tc.want)
			}
		})
	}
}
func TestUsageContextComparisonDoesNotWrap(t *testing.T) {
	max := int(^uint(0) >> 1)
	for _, stop := range []StopReason{StopReasonStop, StopReasonLength} {
		m := AssistantMessage{StopReason: stop, Usage: Usage{Input: max, CacheRead: max}}
		if !IsContextOverflow(m, max) {
			t.Fatalf("wrapped %s heuristic", stop)
		}
	}
	// Cache writes are deliberately not part of this existing heuristic.
	if IsContextOverflow(AssistantMessage{StopReason: StopReasonStop, Usage: Usage{Input: 1, CacheWrite: max}}, 100) {
		t.Fatal("expanded heuristic to cache writes")
	}
	if IsContextOverflow(AssistantMessage{StopReason: StopReasonLength, Usage: Usage{Input: max, CacheRead: max, Output: 1}}, 100) {
		t.Fatal("expanded length heuristic to nonzero output")
	}
}

func TestTokenThresholdComparisonAgainstExactSum(t *testing.T) {
	max := int(^uint(0) >> 1)
	values := []int{0, 1, 99, 100, max / 2, max - 1, max}
	limits := []int{-1, 0, 1, 99, 100, max - 1, max}
	for _, a := range values {
		for _, b := range values {
			for _, c := range values {
				for _, limit := range limits {
					sum := big.NewInt(int64(a))
					sum.Add(sum, big.NewInt(int64(b)))
					sum.Add(sum, big.NewInt(int64(c)))
					want := sum.Cmp(big.NewInt(int64(limit))) > 0
					if got := nonnegativeTokenSumExceeds(limit, a, b, c); got != want {
						t.Fatalf("limit=%d counts=%v got=%v want=%v", limit, []int{a, b, c}, got, want)
					}
				}
			}
		}
	}
	for _, counts := range [][]int{{-1, max, max}, {max, -1, max}, {max, max, -1}} {
		for _, limit := range limits {
			if nonnegativeTokenSumExceeds(limit, counts...) {
				t.Fatalf("negative evidence counts=%v limit=%d", counts, limit)
			}
		}
	}
}

func TestUsageContextComparisonLengthBoundary(t *testing.T) {
	max := int(^uint(0) >> 1)
	for _, window := range []int{1, 2, 100, 1000, max / 2, max} {
		threshold := int(float64(window) * 0.99)
		if threshold < 0 || threshold > window {
			t.Fatalf("invalid existing 99%% threshold: window=%d threshold=%d", window, threshold)
		}
		m := AssistantMessage{StopReason: StopReasonLength, Usage: Usage{Input: threshold}}
		if !IsContextOverflow(m, window) {
			t.Fatalf("missed equality window=%d", window)
		}
		if threshold > 0 {
			m.Usage.Input--
			if IsContextOverflow(m, window) {
				t.Fatalf("below threshold window=%d", window)
			}
		}
	}
	for _, stop := range []StopReason{StopReasonStop, StopReasonLength} {
		for _, u := range []Usage{{Input: max, CacheRead: -1}, {Input: -1, CacheRead: max}} {
			if IsContextOverflow(AssistantMessage{StopReason: stop, Usage: u}, 100) {
				t.Fatal("negative usage evidence accepted")
			}
		}
	}
	model := Model{Cost: UsageCost{Output: 1}, CostTiers: []ModelCostTier{{InputTokensAbove: 100, Rates: UsageCost{Output: 2}}}}
	if c := CalculateCost(model, Usage{Input: max, CacheRead: -1, Output: 1000000}); c.Output != 1 {
		t.Fatal("negative usage selected tier", c)
	}
}
