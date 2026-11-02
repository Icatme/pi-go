package agent

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/pkg/pigo"
)

func contextObservationFixture() pigo.AssistantMessage {
	return pigo.AssistantMessage{StopReason: pigo.StopReasonStop, Content: []pigo.ContentBlock{pigo.TextContent{Text: "answer"}}, Diagnostics: []pigo.AssistantMessageDiagnostic{
		{Type: "kiro_context_usage", Details: map[string]any{"source_event": "contextUsageEvent", "percentage": 42.5, "part_index": 1}},
		{Type: "unrelated", Error: &pigo.DiagnosticErrorInfo{Message: "PRIVATE", Stack: "PRIVATE"}, Details: map[string]any{"raw": "PRIVATE"}},
		{Type: "kiro_context_usage", Details: map[string]any{"source_event": "metadataEvent", "percentage": 43.0}},
	}}
}

func TestPigoContextObservationsWhitelistOwnershipAndPersistence(t *testing.T) {
	original := contextObservationFixture()
	message := convertPigoAssistantMessage(original)
	observations := message.Metadata["kiro_context_usage"].([]any)
	if len(observations) != 2 || observations[0].(map[string]any)["percentage"] != 42.5 || observations[1].(map[string]any)["source_event"] != "metadataEvent" || message.StopReason != StopReasonStop || message.Parts[0].Text != "answer" {
		t.Fatal("mapping")
	}
	encoded, err := json.Marshal(message)
	if err != nil || strings.Contains(string(encoded), "PRIVATE") || strings.Contains(string(encoded), "usage_reported") || strings.Contains(string(encoded), "cost") {
		t.Fatal("non-whitelisted serialization")
	}
	var restored Message
	if json.Unmarshal(encoded, &restored) != nil || restored.Metadata["kiro_context_usage"].([]any)[0].(map[string]any)["percentage"] != 42.5 {
		t.Fatal("metadata persistence")
	}
	original.Diagnostics[0].Details["percentage"] = 99.0
	if observations[0].(map[string]any)["percentage"] != 42.5 {
		t.Fatal("input alias")
	}
	copied := cloneMessage(message)
	copied.Metadata["kiro_context_usage"].([]any)[0].(map[string]any)["percentage"] = 88.0
	if observations[0].(map[string]any)["percentage"] != 42.5 {
		t.Fatal("clone alias")
	}
	replay := convertAssistantMessageToPigo(message).(pigo.AssistantMessage)
	if len(replay.Diagnostics) != 0 || len(replay.Content) != 1 || replay.Content[0].(pigo.TextContent).Text != "answer" {
		t.Fatal("metadata injected into model history")
	}
}

type observationMustNotMarshal struct{}

func (observationMustNotMarshal) MarshalJSON() ([]byte, error) {
	panic("arbitrary diagnostic value marshaled")
}

func TestPigoContextObservationsRejectMalformedWithoutSensitiveValues(t *testing.T) {
	for _, mutate := range []func(*pigo.AssistantMessageDiagnostic){
		func(d *pigo.AssistantMessageDiagnostic) { d.Details["percentage"] = 101.0 }, func(d *pigo.AssistantMessageDiagnostic) { d.Details["percentage"] = math.NaN() }, func(d *pigo.AssistantMessageDiagnostic) { d.Details["percentage"] = math.Copysign(0, -1) }, func(d *pigo.AssistantMessageDiagnostic) { d.Details["percentage"] = json.Number("42.5") }, func(d *pigo.AssistantMessageDiagnostic) { d.Details["percentage"] = observationMustNotMarshal{} }, func(d *pigo.AssistantMessageDiagnostic) { d.Details["part_index"] = 4096 }, func(d *pigo.AssistantMessageDiagnostic) { d.Details["part_index"] = 1.0 }, func(d *pigo.AssistantMessageDiagnostic) { d.Details["source_event"] = "PRIVATE" }, func(d *pigo.AssistantMessageDiagnostic) { d.Details["raw"] = "PRIVATE" }, func(d *pigo.AssistantMessageDiagnostic) { d.Error = &pigo.DiagnosticErrorInfo{Stack: "PRIVATE"} },
	} {
		original := contextObservationFixture()
		mutate(&original.Diagnostics[0])
		message := convertPigoAssistantMessage(original)
		if !reflect.DeepEqual(message.Metadata, map[string]any{"kiro_context_usage_rejected": true}) || message.StopReason != StopReasonStop || message.Parts[0].Text != "answer" {
			t.Fatal("invalid observation changed content or leaked details")
		}
	}
	// A later invalid known record discards all observations, not a plausible prefix.
	original := contextObservationFixture()
	original.Diagnostics[2].Details["extra"] = observationMustNotMarshal{}
	if !reflect.DeepEqual(convertPigoAssistantMessage(original).Metadata, map[string]any{"kiro_context_usage_rejected": true}) {
		t.Fatal("partial metadata retained")
	}
}

func TestPigoContextObservationsFinalOnlyAndLegacyUnchanged(t *testing.T) {
	for _, reason := range []pigo.StopReason{"", pigo.StopReasonError, pigo.StopReasonAborted} {
		original := contextObservationFixture()
		original.StopReason = reason
		if convertPigoAssistantMessage(original).Metadata != nil {
			t.Fatal("nonfinal observation persisted")
		}
	}
	for _, reason := range []pigo.StopReason{pigo.StopReasonStop, pigo.StopReasonLength, pigo.StopReasonToolUse} {
		original := contextObservationFixture()
		original.StopReason = reason
		if convertPigoAssistantMessage(original).Metadata == nil {
			t.Fatal("completed observation lost")
		}
	}
	original := contextObservationFixture()
	original.Diagnostics = original.Diagnostics[1:2]
	if convertPigoAssistantMessage(original).Metadata != nil {
		t.Fatal("unrelated provider diagnostic behavior changed")
	}
	original.Diagnostics = nil
	if convertPigoAssistantMessage(original).Metadata != nil {
		t.Fatal("legacy metadata changed")
	}
}

func TestPigoContextObservationsBoundedRecords(t *testing.T) {
	original := contextObservationFixture()
	record := original.Diagnostics[2]
	original.Diagnostics = make([]pigo.AssistantMessageDiagnostic, 4097)
	for i := range original.Diagnostics {
		original.Diagnostics[i] = record
	}
	message := convertPigoAssistantMessage(original)
	if len(message.Metadata["kiro_context_usage"].([]any)) != 4097 {
		t.Fatal("valid count")
	}
	encoded, err := json.Marshal(message.Metadata)
	if err != nil || len(encoded) > 1<<20 {
		t.Fatal("bounded output")
	}
	original.Diagnostics = append(original.Diagnostics, record)
	if !reflect.DeepEqual(convertPigoAssistantMessage(original).Metadata, map[string]any{"kiro_context_usage_rejected": true}) {
		t.Fatal("oversize retained")
	}
}
