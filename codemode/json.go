package codemode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/Icatme/pi-go/internal/jsontext"
)

// JSONError exposes Unicode and precision failures without parsing diagnostic text.
type JSONError struct{ Code, Message string }

func (e *JSONError) Error() string      { return e.Code + ": " + e.Message }
func unsafeNumber(message string) error { return &JSONError{Code: "unsafe_number", Message: message} }

// ValidateJSON checks Unicode before Go decoding and the Number boundary before
// any JS parse. Invalid UTF-8 and unpaired surrogate escapes are rejected in
// object keys and string values. Ordinary
// decimal JSON roundtrips are accepted; unsafe integers, precision loss,
// overflow and underflow fail. This does not provide decimal arithmetic.
// The caller must apply its byte limit before invoking this function.
func ValidateJSON(raw json.RawMessage) error {
	if len(raw) > 64<<20 {
		return fmt.Errorf("JSON exceeds validation bound")
	}
	if err := jsontext.ValidateUnicode(raw); err != nil {
		if errors.Is(err, jsontext.ErrInvalidUnicode) {
			return &JSONError{Code: "invalid_unicode", Message: "JSON strings must be valid Unicode"}
		}
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	nodes := 0
	if err := validateJSONValue(d, 0, &nodes); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}

func validateJSONValue(d *json.Decoder, depth int, nodes *int) error {
	*nodes++
	if depth > 128 || *nodes > 65536 {
		return fmt.Errorf("JSON nesting or node bound exceeded")
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	switch v := t.(type) {
	case json.Number:
		return safeNumber(string(v))
	case json.Delim:
		if v != '[' && v != '{' {
			return fmt.Errorf("unexpected JSON delimiter")
		}
		for d.More() {
			if v == '{' {
				key, err := d.Token()
				if err != nil {
					return err
				}
				if _, ok := key.(string); !ok {
					return fmt.Errorf("invalid JSON key")
				}
			}
			if err := validateJSONValue(d, depth+1, nodes); err != nil {
				return err
			}
		}
		end, err := d.Token()
		if err != nil {
			return err
		}
		if (v == '[' && end != json.Delim(']')) || (v == '{' && end != json.Delim('}')) {
			return fmt.Errorf("invalid JSON end")
		}
	}
	return nil
}

func safeNumber(s string) error {
	if len(s) > 256 {
		return unsafeNumber("numeric lexeme too long")
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exp, err := strconv.Atoi(s[i+1:])
		if err != nil || exp < -308 || exp > 308 {
			return unsafeNumber("exponent outside supported domain")
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return unsafeNumber("nonfinite Number")
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return unsafeNumber("unsupported Number")
	}
	if r.IsInt() && new(big.Int).Abs(r.Num()).Cmp(big.NewInt(9007199254740991)) > 0 {
		return unsafeNumber("integer exceeds safe range")
	}
	back, ok := new(big.Rat).SetString(strconv.FormatFloat(f, 'g', -1, 64))
	if !ok || r.Cmp(back) != 0 {
		return unsafeNumber("decimal loses precision")
	}
	return nil
}
