package agent

import (
	"encoding/json"
	"math"

	"github.com/Icatme/pi-go/pkg/pigo"
)

// Only this fixed non-secret observation schema crosses into persistent Message
// metadata. Unknown diagnostics, error/stack/raw payloads and custom values never
// cross. These are caller/provider observations, not verified billing or authority.
func pigoContextObservationMetadata(message pigo.AssistantMessage) map[string]any {
	if message.StopReason != pigo.StopReasonStop && message.StopReason != pigo.StopReasonLength && message.StopReason != pigo.StopReasonToolUse {
		return nil
	}
	const maxRecords = 4097 // collector retained-part ceiling plus one metadata snapshot
	inspected := len(message.Diagnostics)
	if inspected > maxRecords {
		inspected = maxRecords
	}
	found := false
	for _, diagnostic := range message.Diagnostics[:inspected] {
		if diagnostic.Type == "kiro_context_usage" {
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	rejected := func() map[string]any { return map[string]any{"kiro_context_usage_rejected": true} }
	if len(message.Diagnostics) > maxRecords {
		return rejected()
	}
	observations := make([]any, 0, len(message.Diagnostics))
	for _, diagnostic := range message.Diagnostics {
		if diagnostic.Type != "kiro_context_usage" {
			continue
		}
		if diagnostic.Error != nil {
			return rejected()
		}
		source, ok := diagnostic.Details["source_event"].(string)
		if !ok {
			return rejected()
		}
		percentage, ok := diagnostic.Details["percentage"].(float64)
		if !ok || math.IsNaN(percentage) || math.IsInf(percentage, 0) || percentage < 0 || percentage > 100 || (percentage == 0 && math.Signbit(percentage)) {
			return rejected()
		}
		observation := map[string]any{"source_event": source, "percentage": percentage}
		switch source {
		case "contextUsageEvent":
			index, ok := diagnostic.Details["part_index"].(int)
			if !ok || index < 0 || index >= 4096 || len(diagnostic.Details) != 3 {
				return rejected()
			}
			observation["part_index"] = index
		case "metadataEvent":
			if len(diagnostic.Details) != 2 {
				return rejected()
			}
		default:
			return rejected()
		}
		observations = append(observations, observation)
	}
	// All values above are owned JSON primitives, never arbitrary diagnostic values.
	// Bound persistent/event output independently of the pigo projection budget.
	metadata := map[string]any{"kiro_context_usage": observations}
	raw, err := json.Marshal(metadata)
	if err != nil || len(raw) > 1<<20 {
		return rejected()
	}
	return metadata
}
