package pigo

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

var errKiroJSON = errors.New("kiro event stream: invalid or ambiguous JSON object")

// decodeKiroJSONObject rejects duplicate keys at every depth before using
// encoding/json's last-key-wins map decoder. Numbers remain raw, not float64.
// maxBytes bounds wire bytes; depth is independently limited to 64 containers.
func decodeKiroJSONObject(payload []byte, maxBytes int) (map[string]json.RawMessage, error) {
	if maxBytes < 2 || len(payload) > maxBytes || !utf8.Valid(payload) || !kiroJSONUnicodeValid(payload) {
		return nil, errKiroJSON
	}
	d := json.NewDecoder(bytes.NewReader(payload))
	d.UseNumber()
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return nil, errKiroJSON
	}
	if err := walkKiroJSONContainer(d, '{', 1); err != nil {
		return nil, errKiroJSON
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return nil, errKiroJSON
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		return nil, errKiroJSON
	}
	return object, nil
}

// encoding/json replaces unpaired UTF-16 escapes with U+FFFD. Reject them
// before decoding identity/argument strings so wire data is never silently
// repaired. Escaped backslashes are skipped, not mistaken for Unicode escapes.
func kiroJSONUnicodeValid(data []byte) bool {
	inString := false
	for i := 0; i < len(data); i++ {
		if data[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || data[i] != '\\' {
			continue
		}
		if i+1 >= len(data) {
			return false
		}
		if data[i+1] != 'u' {
			i++
			continue
		}
		if len(data)-i < 6 {
			return false
		}
		code, ok := kiroJSONHex4(data[i+2 : i+6])
		if !ok || (code >= 0xdc00 && code <= 0xdfff) {
			return false
		}
		if code >= 0xd800 && code <= 0xdbff {
			if len(data)-i < 12 || data[i+6] != '\\' || data[i+7] != 'u' {
				return false
			}
			low, valid := kiroJSONHex4(data[i+8 : i+12])
			if !valid || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 11
		} else {
			i += 5
		}
	}
	return true
}

func kiroJSONHex4(digits []byte) (uint16, bool) {
	var value uint16
	for _, digit := range digits {
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value += uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value += uint16(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			value += uint16(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func walkKiroJSONContainer(d *json.Decoder, open json.Delim, depth int) error {
	if depth > 64 {
		return errKiroJSON
	}
	keys := make(map[string]struct{})
	for d.More() {
		if open == '{' {
			key, err := d.Token()
			name, ok := key.(string)
			if err != nil || !ok {
				return errKiroJSON
			}
			if _, duplicate := keys[name]; duplicate {
				return errKiroJSON
			}
			keys[name] = struct{}{}
		}
		value, err := d.Token()
		if err != nil {
			return errKiroJSON
		}
		if delimiter, ok := value.(json.Delim); ok {
			if delimiter != '{' && delimiter != '[' {
				return errKiroJSON
			}
			if err := walkKiroJSONContainer(d, delimiter, depth+1); err != nil {
				return err
			}
		}
	}
	close, err := d.Token()
	want := json.Delim('}')
	if open == '[' {
		want = ']'
	}
	if err != nil || close != want {
		return errKiroJSON
	}
	return nil
}
