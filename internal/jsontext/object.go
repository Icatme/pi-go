package jsontext

import (
	"bytes"
	"encoding/json"
	"errors"
)

var ErrDuplicateName = errors.New("duplicate JSON object member")

// ValidateObject rejects ambiguous names (including escape-equivalent names)
// before a map decoder can discard them. maxDepth counts the root object.
func ValidateObject(raw []byte, maxDepth int) error {
	if err := ValidateUnicode(raw); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return ErrInvalidJSON
	}
	return validateContainer(d, '{', maxDepth)
}

func validateContainer(d *json.Decoder, open json.Delim, remaining int) error {
	if remaining <= 0 {
		return ErrValueLimit
	}
	seen := map[string]struct{}{}
	for d.More() {
		if open == '{' {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name := key.(string) // ValidateUnicode already checked the JSON grammar.
			if _, duplicate := seen[name]; duplicate {
				return ErrDuplicateName
			}
			seen[name] = struct{}{}
		}
		value, err := d.Token()
		if err != nil {
			return err
		}
		if delimiter, ok := value.(json.Delim); ok {
			if err := validateContainer(d, delimiter, remaining-1); err != nil {
				return err
			}
		}
	}
	_, err := d.Token()
	return err
}
