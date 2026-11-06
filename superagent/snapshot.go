package superagent

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/Icatme/pi-go/agent"
)

// validatedSnapshot belongs only to the coordinator's current transition. Its
// decoded value has no callback-owned aliases and is never exposed or mutated.
// raw is an immutable copy binding that value to the exact persisted bytes;
// byte equality is cheap compared with decoding, and does not need a hash.
// A proof is not a state/authorization decision and is never written to disk.
type validatedSnapshot struct {
	raw   string
	value agent.AgentSnapshot
}

func encodeSnapshot(s agent.AgentSnapshot, taskID string) (data json.RawMessage, validated *validatedSnapshot, err error) {
	defer func() {
		if p := recover(); p != nil {
			data, validated = nil, nil
			err = callbackPanic("worker snapshot serialization", p)
		}
	}()
	if !belongsTo(s, taskID) {
		return nil, nil, errors.New("worker returned a snapshot with incorrect task identity")
	}
	data, err = json.Marshal(s)
	if err != nil {
		return nil, nil, err
	}
	if len(data) > maxSnapshotBytes {
		return nil, nil, errors.New("worker snapshot exceeds 8 MiB")
	}
	// Validate the encoded form, not callback memory: nested custom MarshalJSON
	// values can encode data rejected by Agent's stricter tool argument parser.
	decoded, err := validateSnapshot(data, taskID, nil)
	if err != nil {
		return nil, nil, err
	}
	return data, &validatedSnapshot{raw: string(data), value: decoded}, nil
}

// validateSnapshot preserves content and owner checks at every boundary. Only
// an exact match to our private, immutable encoding may reuse a parsed value.
// Replay supplies no proof; changed bytes cannot borrow a prior validation.
func validateSnapshot(data json.RawMessage, taskID string, validated *validatedSnapshot) (agent.AgentSnapshot, error) {
	if len(data) == 0 || len(data) > maxSnapshotBytes || data[0] != '{' {
		return agent.AgentSnapshot{}, errors.New("invalid snapshot encoding")
	}
	if validated != nil {
		if validated.raw != string(data) {
			return agent.AgentSnapshot{}, errors.New("snapshot changed after validation")
		}
		if !belongsTo(validated.value, taskID) {
			return agent.AgentSnapshot{}, errors.New("snapshot belongs to another session")
		}
		return validated.value, nil
	}
	var decoded agent.AgentSnapshot
	if err := decodeSnapshot(data, &decoded); err != nil {
		return decoded, err
	}
	if !belongsTo(decoded, taskID) {
		return agent.AgentSnapshot{}, errors.New("snapshot belongs to another session")
	}
	return decoded, nil
}

func decodeSnapshot(data json.RawMessage, dst *agent.AgentSnapshot) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(dst)
}
