package pigo

import (
	"bytes"
	"encoding/json"
	"errors"
)

var errKiroToolAssembly = errors.New("kiro event stream: invalid or incomplete tool sequence")

// kiroAssembledTool is inert protocol data, not an executable pi-go ToolCall.
// Name/schema authorization and agent-layer execution are outside this type.
type kiroAssembledTool struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// kiroToolAssembler accepts sequential structured tool events only. There is
// no EOF flush, prose extraction, parallel-ID guessing, retry or tool execution.
// A single goroutine owns it. All errors are terminal; finish is terminal too.
type kiroToolAssembler struct {
	maxArguments  int
	maxEventBytes int
	maxEvents     int
	maxTools      int
	events        int
	seen          map[string]struct{}
	active        *kiroAssembledTool
	terminal      error
	finished      bool
}

func newKiroToolAssembler(maxArguments, maxEventBytes, maxEvents, maxTools int) (*kiroToolAssembler, error) {
	if maxArguments < 2 || maxEventBytes < 2 || maxEvents < 1 || maxTools < 1 {
		return nil, errKiroToolAssembly
	}
	return &kiroToolAssembler{maxArguments: maxArguments, maxEventBytes: maxEventBytes, maxEvents: maxEvents, maxTools: maxTools, seen: make(map[string]struct{})}, nil
}

// accept consumes a classified toolUseEvent payload, not arbitrary text. Each
// payload must already fit the framing limit. An explicit stop is required for
// completion, even when the argument object is syntactically complete earlier.
func (a *kiroToolAssembler) accept(payload []byte) (*kiroAssembledTool, error) {
	if a.terminal != nil {
		return nil, a.terminal
	}
	if a.finished || a.events >= a.maxEvents {
		return nil, a.fail()
	}
	a.events++
	fields, err := decodeKiroJSONObject(payload, a.maxEventBytes)
	if err != nil {
		return nil, a.fail()
	}
	for key := range fields {
		if key != "name" && key != "toolUseId" && key != "input" && key != "stop" {
			return nil, a.fail()
		}
	}
	nameRaw, hasName := fields["name"]
	idRaw, hasID := fields["toolUseId"]
	if hasName != hasID {
		return nil, a.fail()
	}
	if hasName {
		var name, id string
		if json.Unmarshal(nameRaw, &name) != nil || json.Unmarshal(idRaw, &id) != nil || name == "" || len(name) > 256 || !kiroNativeToolID(id) {
			return nil, a.fail()
		}
		if a.active != nil {
			if a.active.ID != id || a.active.Name != name {
				return nil, a.fail()
			}
		} else {
			if _, duplicate := a.seen[id]; duplicate || len(a.seen) >= a.maxTools {
				return nil, a.fail()
			}
			a.seen[id] = struct{}{}
			a.active = &kiroAssembledTool{ID: id, Name: name}
		}
	}
	if a.active == nil {
		return nil, a.fail()
	}
	input, hasInput := fields["input"]
	stopRaw, hasStop := fields["stop"]
	if !hasName && !hasInput && !hasStop {
		return nil, a.fail()
	}
	if hasInput {
		trimmed := bytes.TrimSpace(input)
		var fragment []byte
		if len(trimmed) > 0 && trimmed[0] == '"' {
			var text string
			if json.Unmarshal(input, &text) != nil {
				return nil, a.fail()
			}
			fragment = []byte(text)
		} else {
			object, err := decodeKiroJSONObject(input, len(input))
			if err != nil {
				return nil, a.fail()
			}
			// The upstream sends {} as an empty placeholder before fragments.
			if len(object) != 0 {
				fragment = trimmed
			}
		}
		if len(fragment) > a.maxArguments-len(a.active.Arguments) {
			return nil, a.fail()
		}
		a.active.Arguments = append(a.active.Arguments, fragment...)
	}
	stop := false
	if hasStop {
		// Unmarshal(null, *bool) succeeds, so require the literal boolean.
		trimmed := bytes.TrimSpace(stopRaw)
		if !bytes.Equal(trimmed, []byte("true")) && !bytes.Equal(trimmed, []byte("false")) {
			return nil, a.fail()
		}
		stop = bytes.Equal(trimmed, []byte("true"))
	}
	if !stop {
		return nil, nil
	}
	if len(a.active.Arguments) == 0 {
		a.active.Arguments = json.RawMessage("{}")
	}
	if _, err := decodeKiroJSONObject(a.active.Arguments, a.maxArguments); err != nil {
		return nil, a.fail()
	}
	complete := a.active
	a.active = nil
	return complete, nil
}

func (a *kiroToolAssembler) finish() error {
	if a.terminal != nil {
		return a.terminal
	}
	if a.active != nil {
		return a.fail()
	}
	a.finished = true
	return nil
}

func (a *kiroToolAssembler) fail() error {
	a.active = nil
	a.terminal = errKiroToolAssembly
	return a.terminal
}

// kiroNativeToolID is shared by receive and replay: no hashing or repair.
func kiroNativeToolID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, c := range []byte(id) {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '.' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}
