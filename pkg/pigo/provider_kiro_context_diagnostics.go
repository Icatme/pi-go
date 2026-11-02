package pigo

import (
	"encoding/json"
	"errors"
	"math"
	"time"
)

var errKiroContextDiagnostics = errors.New("kiro: invalid completed context observations")

// splitKiroContextObservations preserves percentages as diagnostics, never token
// counts/cost or completion evidence. part_index is the collector's retained-part
// position, NOT a raw frame index. Metadata has no known position. Diagnostic
// JSON bytes consume the same bounded projection payload budget as text/tools.
// Only completed projectors use this; it does not emit provisional observations.
func splitKiroContextObservations(response kiroOfflineResponse, stamp time.Time, maxBytes int) (kiroOfflineResponse, []AssistantMessageDiagnostic, int, error) {
	bad := func() (kiroOfflineResponse, []AssistantMessageDiagnostic, int, error) {
		return kiroOfflineResponse{}, nil, 0, errKiroContextDiagnostics
	}
	if maxBytes < 1 || maxBytes > 1<<20 || len(response.Parts) > 4096 {
		return bad()
	}
	out := response
	out.Parts = make([]kiroOfflinePart, 0, len(response.Parts))
	var diagnostics []AssistantMessageDiagnostic
	remaining := maxBytes
	add := func(value float64, source string, index int) bool {
		if math.IsNaN(value) || math.IsInf(value, 0) || (value == 0 && math.Signbit(value)) || value < 0 || value > 100 {
			return false
		}
		details := map[string]any{"source_event": source, "percentage": value}
		if index >= 0 {
			details["part_index"] = index
		}
		diagnostic := AssistantMessageDiagnostic{Type: "kiro_context_usage", Timestamp: stamp, Details: details}
		raw, err := json.Marshal(diagnostic)
		if err != nil || len(raw) > remaining {
			return false
		}
		remaining -= len(raw)
		diagnostics = append(diagnostics, diagnostic)
		return true
	}
	for index, part := range response.Parts {
		if part.Kind != "context_usage" {
			out.Parts = append(out.Parts, part)
			continue
		}
		if part.ContextUsagePercentage == nil || part.Tool != nil || part.Text != "" || part.Signature != "" || !add(*part.ContextUsagePercentage, "contextUsageEvent", index) {
			return bad()
		}
	}
	if response.Usage.ContextUsagePercentage != nil {
		if !add(*response.Usage.ContextUsagePercentage, "metadataEvent", -1) {
			return bad()
		}
		out.Usage.ContextUsagePercentage = nil
	}
	return out, diagnostics, remaining, nil
}
