package pigo

import (
	"encoding/json"
	"errors"

	"github.com/Icatme/pi-go/internal/jsontext"
)

var errKiroJSON = errors.New("kiro event stream: invalid or ambiguous JSON object")

// decodeKiroJSONObject rejects duplicate keys at every depth before using
// encoding/json's last-key-wins map decoder. Numbers remain raw, not float64.
// maxBytes bounds wire bytes; depth is independently limited to 64 containers.
func decodeKiroJSONObject(payload []byte, maxBytes int) (map[string]json.RawMessage, error) {
	if maxBytes < 2 || len(payload) > maxBytes || jsontext.ValidateObject(payload, 64) != nil {
		return nil, errKiroJSON
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		return nil, errKiroJSON
	}
	return object, nil
}
