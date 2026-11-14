package pigo

import (
	"strconv"
	"time"
)

// The fixed-size evidence is diagnostic, not a billing interface. Decimal strings
// preserve exact integer counts through Context JSON's generic details decoder.
func appendAnthropicUsageAggregateDiagnostic(response *AssistantMessage, previous, current Usage) {
	diagnostic := AssistantMessageDiagnostic{
		Type:      "anthropic_usage_aggregate",
		Timestamp: time.Now().UTC(),
		Details: map[string]any{
			"usage_scope":    "previous_requests_only",
			"previous_usage": anthropicUsageEvidence(previous),
			"request_usage":  anthropicUsageEvidence(current),
		},
	}
	response.Diagnostics = append(append([]AssistantMessageDiagnostic(nil), response.Diagnostics...), diagnostic)
}

func anthropicUsageEvidence(u Usage) map[string]any {
	return map[string]any{
		"input": strconv.Itoa(u.Input), "output": strconv.Itoa(u.Output),
		"cache_read": strconv.Itoa(u.CacheRead), "cache_write": strconv.Itoa(u.CacheWrite), "total": strconv.Itoa(u.TotalTokens),
		"cost_input":       strconv.FormatFloat(u.Cost.Input, 'g', -1, 64),
		"cost_output":      strconv.FormatFloat(u.Cost.Output, 'g', -1, 64),
		"cost_cache_read":  strconv.FormatFloat(u.Cost.CacheRead, 'g', -1, 64),
		"cost_cache_write": strconv.FormatFloat(u.Cost.CacheWrite, 'g', -1, 64),
		"cost_total":       strconv.FormatFloat(u.Cost.Total, 'g', -1, 64),
	}
}
