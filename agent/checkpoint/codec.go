package checkpoint

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/Icatme/pi-go/agent"
)

const (
	checkpointEnvelopeKind    = "pi-go.agent.checkpoint"
	checkpointEnvelopeVersion = 1
	maxJSONDepth              = 256
)

var jsonRawMessageType = reflect.TypeFor[json.RawMessage]()

type checkpointEnvelope struct {
	Kind              string              `json:"kind"`
	Version           int                 `json:"version"`
	CheckpointID      CheckpointID        `json:"checkpoint_id"`
	DefinitionVersion string              `json:"definition_version"`
	Revision          Revision            `json:"revision"`
	Status            Status              `json:"status"`
	Snapshot          agent.AgentSnapshot `json:"snapshot"`
	Interrupts        []storedInterrupt   `json:"interrupts,omitempty"`
	Decisions         []storedDecision    `json:"decisions,omitempty"`
	Error             string              `json:"error,omitempty"`
}

type storedInterrupt struct {
	Interrupt Interrupt `json:"interrupt"`
	Digest    string    `json:"digest"`
}

type storedDecision struct {
	Digest   string         `json:"digest"`
	Decision ResumeDecision `json:"decision"`
}

func newEnvelope(id CheckpointID, definitionVersion string, snapshot agent.AgentSnapshot) checkpointEnvelope {
	return checkpointEnvelope{
		Kind:              checkpointEnvelopeKind,
		Version:           checkpointEnvelopeVersion,
		CheckpointID:      id,
		DefinitionVersion: definitionVersion,
		Status:            StatusRunning,
		Snapshot:          snapshot,
	}
}

func encodeEnvelope(envelope checkpointEnvelope) ([]byte, error) {
	if err := validateEnvelope(envelope); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("%w: encode payload: %v", ErrInvalidCheckpoint, err)
	}
	if len(payload) > MaxPayloadBytes {
		return nil, fmt.Errorf("%w: %d bytes exceeds %d", ErrPayloadTooLarge, len(payload), MaxPayloadBytes)
	}
	if err := rejectDuplicateJSONKeys(payload); err != nil {
		return nil, fmt.Errorf("%w: encoded payload: %v", ErrInvalidCheckpoint, err)
	}
	return payload, nil
}

func decodeStoredCheckpoint(id CheckpointID, record StoredCheckpoint) (checkpointEnvelope, error) {
	if len(record.Payload) > MaxPayloadBytes {
		return checkpointEnvelope{}, fmt.Errorf("%w: %d bytes exceeds %d", ErrPayloadTooLarge, len(record.Payload), MaxPayloadBytes)
	}
	if len(record.Payload) == 0 {
		return checkpointEnvelope{}, fmt.Errorf("%w: empty payload", ErrInvalidCheckpoint)
	}
	if err := rejectDuplicateJSONKeys(record.Payload); err != nil {
		return checkpointEnvelope{}, fmt.Errorf("%w: %v", ErrInvalidCheckpoint, err)
	}
	if err := rejectInexactJSONFields(record.Payload, reflect.TypeFor[checkpointEnvelope]()); err != nil {
		return checkpointEnvelope{}, fmt.Errorf("%w: %v", ErrInvalidCheckpoint, err)
	}

	decoder := json.NewDecoder(bytes.NewReader(record.Payload))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	var envelope checkpointEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return checkpointEnvelope{}, fmt.Errorf("%w: decode payload: %v", ErrInvalidCheckpoint, err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return checkpointEnvelope{}, fmt.Errorf("%w: trailing payload: %v", ErrInvalidCheckpoint, err)
	}
	if envelope.Revision != record.Revision {
		return checkpointEnvelope{}, fmt.Errorf("%w: envelope revision %d does not match store revision %d", ErrInvalidCheckpoint, envelope.Revision, record.Revision)
	}
	if envelope.CheckpointID != id {
		return checkpointEnvelope{}, fmt.Errorf("%w: payload checkpoint id %q does not match store key %q", ErrInvalidCheckpoint, envelope.CheckpointID, id)
	}
	if err := validateEnvelope(envelope); err != nil {
		return checkpointEnvelope{}, err
	}
	return envelope, nil
}

func rejectDuplicateJSONKeys(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := scanJSONValue(decoder, 0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func rejectInexactJSONFields(payload []byte, target reflect.Type) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return err
	}
	return validateExactJSONFields(target, value, "$")
}

func validateExactJSONFields(target reflect.Type, value any, path string) error {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if value == nil || isOpaqueJSONType(target) || target.Kind() == reflect.Interface {
		return nil
	}

	switch target.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		fields := exactJSONStructFields(target)
		for key, child := range object {
			fieldType, ok := fields[key]
			if !ok {
				return fmt.Errorf("field %s.%s does not exactly match a JSON field name", path, key)
			}
			if err := validateExactJSONFields(fieldType, child, path+"."+key); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		array, ok := value.([]any)
		if !ok {
			return nil
		}
		for index, child := range array {
			if err := validateExactJSONFields(target.Elem(), child, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		for key, child := range object {
			if err := validateExactJSONFields(target.Elem(), child, path+"."+key); err != nil {
				return err
			}
		}
	}
	return nil
}

func exactJSONStructFields(target reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type)
	for index := range target.NumField() {
		field := target.Field(index)
		if !field.IsExported() {
			continue
		}
		tag := field.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
	}
	return fields
}

func isOpaqueJSONType(target reflect.Type) bool {
	return target == jsonRawMessageType
}

func scanJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return fmt.Errorf("JSON nesting exceeds maximum depth %d", maxJSONDepth)
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate object key %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("array is not closed")
		}
	default:
		return fmt.Errorf("unexpected delimiter %q", delimiter)
	}
	return nil
}

func validateEnvelope(envelope checkpointEnvelope) error {
	if envelope.Kind != checkpointEnvelopeKind {
		return fmt.Errorf("%w: unexpected kind %q", ErrInvalidCheckpoint, envelope.Kind)
	}
	if envelope.Version != checkpointEnvelopeVersion {
		return fmt.Errorf("%w: unsupported envelope version %d", ErrInvalidCheckpoint, envelope.Version)
	}
	if strings.TrimSpace(envelope.DefinitionVersion) == "" {
		return fmt.Errorf("%w: empty definition version", ErrInvalidCheckpoint)
	}
	if err := validateCheckpointID(envelope.CheckpointID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCheckpoint, err)
	}
	if envelope.Revision == 0 {
		return fmt.Errorf("%w: zero revision", ErrInvalidCheckpoint)
	}
	switch envelope.Status {
	case StatusRunning:
		if len(envelope.Interrupts) != 0 {
			return fmt.Errorf("%w: status %q cannot have active interrupts", ErrInvalidCheckpoint, envelope.Status)
		}
		if envelope.Error != "" {
			return fmt.Errorf("%w: status %q cannot have an error", ErrInvalidCheckpoint, envelope.Status)
		}
		if len(envelope.Snapshot.PendingToolCalls) > 0 {
			if err := validatePendingSnapshot(envelope.Snapshot); err != nil {
				return fmt.Errorf("%w: running snapshot: %v", ErrInvalidCheckpoint, err)
			}
		} else if envelope.Snapshot.PendingToolControl != nil {
			return fmt.Errorf("%w: running snapshot has control state without pending tool calls", ErrInvalidCheckpoint)
		}
	case StatusCompleted:
		if len(envelope.Interrupts) != 0 || len(envelope.Snapshot.PendingToolCalls) != 0 || envelope.Snapshot.PendingToolControl != nil {
			return fmt.Errorf("%w: completed checkpoint cannot have active or pending interrupts", ErrInvalidCheckpoint)
		}
		if envelope.Error != "" {
			return fmt.Errorf("%w: completed checkpoint cannot have an error", ErrInvalidCheckpoint)
		}
	case StatusFailed, StatusIndeterminate:
		if len(envelope.Interrupts) != 0 || len(envelope.Snapshot.PendingToolCalls) != 0 || envelope.Snapshot.PendingToolControl != nil || envelope.Error == "" {
			return fmt.Errorf("%w: status %q requires an error and no active or pending interrupts", ErrInvalidCheckpoint, envelope.Status)
		}
	case StatusInterrupted:
		if len(envelope.Interrupts) == 0 {
			return fmt.Errorf("%w: interrupted checkpoint has no active interrupts", ErrInvalidCheckpoint)
		}
		if envelope.Error != "" {
			return fmt.Errorf("%w: interrupted checkpoint cannot have an error", ErrInvalidCheckpoint)
		}
		if err := validatePendingSnapshot(envelope.Snapshot); err != nil {
			return fmt.Errorf("%w: interrupted snapshot: %v", ErrInvalidCheckpoint, err)
		}
	default:
		return fmt.Errorf("%w: unknown status %q", ErrInvalidCheckpoint, envelope.Status)
	}

	interruptIDs := make(map[InterruptID]struct{}, len(envelope.Interrupts))
	interruptDigests := make(map[string]struct{}, len(envelope.Interrupts))
	interruptTargets := make(map[string]struct{}, len(envelope.Interrupts))
	for _, stored := range envelope.Interrupts {
		interrupt := stored.Interrupt
		if strings.TrimSpace(string(interrupt.ID)) == "" || !validApprovalDigest(stored.Digest) {
			return fmt.Errorf("%w: incomplete interrupt", ErrInvalidCheckpoint)
		}
		if interrupt.Kind != InterruptKindToolApproval || interrupt.Tool.ToolCallID == "" || interrupt.Tool.ToolName == "" {
			return fmt.Errorf("%w: invalid tool approval interrupt", ErrInvalidCheckpoint)
		}
		if !json.Valid(interrupt.Tool.Arguments) {
			return fmt.Errorf("%w: invalid approval arguments", ErrInvalidCheckpoint)
		}
		canonical, err := canonicalJSON(interrupt.Tool.Arguments)
		if err != nil || !bytes.Equal(canonical, interrupt.Tool.Arguments) {
			return fmt.Errorf("%w: approval arguments are not canonical", ErrInvalidCheckpoint)
		}
		if _, duplicate := interruptIDs[interrupt.ID]; duplicate {
			return fmt.Errorf("%w: duplicate interrupt id %q", ErrInvalidCheckpoint, interrupt.ID)
		}
		if _, duplicate := interruptDigests[stored.Digest]; duplicate {
			return fmt.Errorf("%w: duplicate interrupt digest", ErrInvalidCheckpoint)
		}
		interruptIDs[interrupt.ID] = struct{}{}
		interruptDigests[stored.Digest] = struct{}{}
		target := interrupt.Tool.ToolCallID + "\x00" + interrupt.Tool.OriginalToolCallID + "\x00" + interrupt.Tool.ToolName
		if _, duplicate := interruptTargets[target]; duplicate {
			return fmt.Errorf("%w: multiple active interrupts target tool call %q", ErrInvalidCheckpoint, interrupt.Tool.ToolCallID)
		}
		interruptTargets[target] = struct{}{}
		if !approvalMatchesPending(interrupt.Tool, envelope.Snapshot.PendingToolCalls) {
			return fmt.Errorf("%w: interrupt %q does not match a pending tool call", ErrInvalidCheckpoint, interrupt.ID)
		}
		if err := validateApprovalBinding(envelope.DefinitionVersion, stored, envelope.Snapshot); err != nil {
			return fmt.Errorf("%w: interrupt %q binding: %v", ErrInvalidCheckpoint, interrupt.ID, err)
		}
	}

	decisionDigests := make(map[string]struct{}, len(envelope.Decisions))
	for _, stored := range envelope.Decisions {
		if !validApprovalDigest(stored.Digest) || !validDecisionAction(stored.Decision.Action) {
			return fmt.Errorf("%w: invalid stored decision", ErrInvalidCheckpoint)
		}
		if _, duplicate := decisionDigests[stored.Digest]; duplicate {
			return fmt.Errorf("%w: duplicate decision digest", ErrInvalidCheckpoint)
		}
		decisionDigests[stored.Digest] = struct{}{}
	}
	for digest := range interruptDigests {
		if _, decided := decisionDigests[digest]; decided {
			return fmt.Errorf("%w: active interrupt already has a decision", ErrInvalidCheckpoint)
		}
	}
	return nil
}

func validApprovalDigest(digest string) bool {
	if len(digest) != sha256DigestHexLength {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func normalizeSnapshot(snapshot agent.AgentSnapshot) (agent.AgentSnapshot, error) {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return agent.AgentSnapshot{}, fmt.Errorf("%w: snapshot is not durable: %v", ErrInvalidCheckpoint, err)
	}
	if len(payload) > MaxPayloadBytes {
		return agent.AgentSnapshot{}, fmt.Errorf("%w: snapshot exceeds payload limit", ErrPayloadTooLarge)
	}
	if err := rejectDuplicateJSONKeys(payload); err != nil {
		return agent.AgentSnapshot{}, fmt.Errorf("%w: snapshot JSON: %v", ErrInvalidCheckpoint, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	var normalized agent.AgentSnapshot
	if err := decoder.Decode(&normalized); err != nil {
		return agent.AgentSnapshot{}, fmt.Errorf("%w: normalize snapshot: %v", ErrInvalidCheckpoint, err)
	}
	return normalized, nil
}

func normalizeMessages(messages []agent.Message) ([]agent.Message, error) {
	payload, err := json.Marshal(messages)
	if err != nil {
		return nil, fmt.Errorf("%w: prompt messages are not durable: %v", ErrInvalidCheckpoint, err)
	}
	if len(payload) > MaxPayloadBytes {
		return nil, fmt.Errorf("%w: prompt messages exceed payload limit", ErrPayloadTooLarge)
	}
	if err := rejectDuplicateJSONKeys(payload); err != nil {
		return nil, fmt.Errorf("%w: prompt message JSON: %v", ErrInvalidCheckpoint, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	var normalized []agent.Message
	if err := decoder.Decode(&normalized); err != nil {
		return nil, fmt.Errorf("%w: normalize prompt messages: %v", ErrInvalidCheckpoint, err)
	}
	return normalized, nil
}

func cloneOutcome(outcome Outcome) Outcome {
	cloned := outcome
	if snapshot, err := normalizeSnapshot(outcome.Snapshot); err == nil {
		cloned.Snapshot = snapshot
	} else {
		cloned.Snapshot = agent.AgentSnapshot{}
	}
	cloned.Interrupts = cloneInterrupts(outcome.Interrupts)
	return cloned
}

func validatePendingSnapshot(snapshot agent.AgentSnapshot) error {
	return agent.ValidatePendingToolState(snapshot)
}

func approvalMatchesPending(request ToolApprovalRequest, pending []agent.PendingToolCall) bool {
	for _, call := range pending {
		if request.ToolCallID == call.ToolCallID && request.OriginalToolCallID == call.OriginalToolCallID && request.ToolName == call.ToolName {
			return true
		}
	}
	return false
}

func validateApprovalBinding(definitionVersion string, stored storedInterrupt, snapshot agent.AgentSnapshot) error {
	if len(snapshot.Messages) == 0 {
		return errors.New("missing assistant tool-call message")
	}
	tail := snapshot.Messages[len(snapshot.Messages)-1]
	var call *agent.ToolCall
	for index := range tail.ToolCalls {
		candidate := &tail.ToolCalls[index]
		if candidate.ID == stored.Interrupt.Tool.ToolCallID && candidate.OriginalID == stored.Interrupt.Tool.OriginalToolCallID && candidate.Name == stored.Interrupt.Tool.ToolName {
			call = candidate
			break
		}
	}
	if call == nil {
		return errors.New("approval request does not identify an assistant tool call")
	}
	decoder := json.NewDecoder(bytes.NewReader(stored.Interrupt.Tool.Arguments))
	decoder.UseNumber()
	var args any
	if err := decoder.Decode(&args); err != nil {
		return fmt.Errorf("decode displayed arguments: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return fmt.Errorf("decode displayed arguments: %w", err)
	}
	recomputed, err := makePendingApproval(definitionVersion, *call, args)
	if err != nil {
		return err
	}
	if recomputed.digest != stored.Digest {
		return errors.New("digest does not match the displayed request and assistant call")
	}
	if !bytes.Equal(recomputed.request.Arguments, stored.Interrupt.Tool.Arguments) {
		return errors.New("displayed arguments do not match the digest input")
	}
	return nil
}

func cloneInterrupts(interrupts []Interrupt) []Interrupt {
	if len(interrupts) == 0 {
		return nil
	}
	cloned := make([]Interrupt, len(interrupts))
	for index, interrupt := range interrupts {
		cloned[index] = interrupt
		cloned[index].Tool.Arguments = append(json.RawMessage(nil), interrupt.Tool.Arguments...)
	}
	return cloned
}

func outcomeFromEnvelope(id CheckpointID, envelope checkpointEnvelope) Outcome {
	return outcomeFromEnvelopeWithPersistence(id, envelope, true)
}

func transientOutcomeFromEnvelope(id CheckpointID, envelope checkpointEnvelope) Outcome {
	return outcomeFromEnvelopeWithPersistence(id, envelope, false)
}

func outcomeFromEnvelopeWithPersistence(id CheckpointID, envelope checkpointEnvelope, persisted bool) Outcome {
	interrupts := make([]Interrupt, 0, len(envelope.Interrupts))
	for _, stored := range envelope.Interrupts {
		interrupts = append(interrupts, stored.Interrupt)
	}
	return cloneOutcome(Outcome{
		CheckpointID: id,
		Revision:     envelope.Revision,
		Status:       envelope.Status,
		Persisted:    persisted,
		Snapshot:     envelope.Snapshot,
		Interrupts:   interrupts,
	})
}

func validDecisionAction(action DecisionAction) bool {
	return action == DecisionActionApprove || action == DecisionActionReject
}
