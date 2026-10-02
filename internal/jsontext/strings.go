// Package jsontext checks strings before encoding/json can replace invalid
// UTF-8 or unpaired UTF-16 surrogate escapes with the replacement rune.
package jsontext

import (
	"encoding/json"
	"errors"
	"reflect"
	"unicode/utf8"
)

var (
	ErrInvalidUnicode = errors.New("invalid_unicode: JSON strings must be valid Unicode")
	ErrInvalidJSON    = errors.New("invalid JSON value")
	ErrValueLimit     = errors.New("JSON string validation nesting or node bound exceeded")
)

// ValidateUnicode checks raw JSON keys and values without decoding strings.
// Callers apply their byte bound first. json.Valid supplies the JSON grammar;
// this scan only enforces the Unicode condition it deliberately does not check.
func ValidateUnicode(raw []byte) error {
	if !utf8.Valid(raw) {
		return ErrInvalidUnicode
	}
	if !json.Valid(raw) {
		return ErrInvalidJSON
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] != '"' {
			continue
		}
		for i++; raw[i] != '"'; i++ {
			if raw[i] != '\\' {
				continue
			}
			i++
			if raw[i] != 'u' {
				continue
			}
			unit := hexUnit(raw[i+1 : i+5])
			i += 4
			if unit >= 0xdc00 && unit <= 0xdfff {
				return ErrInvalidUnicode
			}
			if unit < 0xd800 || unit > 0xdbff {
				continue
			}
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return ErrInvalidUnicode
			}
			low := hexUnit(raw[i+3 : i+7])
			if low < 0xdc00 || low > 0xdfff {
				return ErrInvalidUnicode
			}
			i += 6
		}
	}
	return nil
}

func hexUnit(raw []byte) uint16 {
	var value uint16
	for _, c := range raw {
		value <<= 4
		switch {
		case c >= '0' && c <= '9':
			value |= uint16(c - '0')
		case c >= 'a' && c <= 'f':
			value |= uint16(c-'a') + 10
		default:
			value |= uint16(c-'A') + 10
		}
	}
	return value
}

// ValidateStrings checks Go string values and keys before json.Marshal's UTF-8
// replacement. RawMessage values receive the same raw check. Encoding and its
// type rules remain encoding/json's responsibility; this adds no serializer.
func ValidateStrings(value any) error {
	nodes := 0
	return validateStrings(reflect.ValueOf(value), 0, &nodes)
}

func validateStrings(value reflect.Value, depth int, nodes *int) error {
	if !value.IsValid() {
		return nil
	}
	if depth > 128 {
		return ErrValueLimit
	}
	// Go interface/pointer wrappers do not add JSON nesting. Count their work,
	// but unwrap iteratively so a pointer cycle cannot grow the native stack.
	for {
		*nodes++
		if *nodes > 4*65536 {
			return ErrValueLimit
		}
		if value.Kind() != reflect.Interface && value.Kind() != reflect.Pointer {
			break
		}
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	if value.Type() == reflect.TypeFor[json.RawMessage]() {
		if value.IsNil() {
			return nil
		}
		return ValidateUnicode(value.Bytes())
	}
	switch value.Kind() {
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return ErrInvalidUnicode
		}
	case reflect.Map:
		iterator := value.MapRange()
		for iterator.Next() {
			if err := validateStrings(iterator.Key(), depth+1, nodes); err != nil {
				return err
			}
			if err := validateStrings(iterator.Value(), depth+1, nodes); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		// Ordinary byte slices encode as base64 rather than JSON strings.
		if value.Kind() == reflect.Slice && value.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		for i := 0; i < value.Len(); i++ {
			if err := validateStrings(value.Index(i), depth+1, nodes); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			field := value.Type().Field(i)
			if field.PkgPath == "" && field.Tag.Get("json") != "-" {
				if err := validateStrings(value.Field(i), depth+1, nodes); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
