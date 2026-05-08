package pigo

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

var validJSONEscapes = map[rune]bool{
	'"':  true,
	'\\': true,
	'/':  true,
	'b':  true,
	'f':  true,
	'n':  true,
	'r':  true,
	't':  true,
	'u':  true,
}

func isControlCharacter(r rune) bool {
	return r >= 0x00 && r <= 0x1f
}

func escapeControlCharacter(r rune) string {
	switch r {
	case '\b':
		return "\\b"
	case '\f':
		return "\\f"
	case '\n':
		return "\\n"
	case '\r':
		return "\\r"
	case '\t':
		return "\\t"
	default:
		return fmt.Sprintf("\\u%04x", r)
	}
}

func repairJSON(input string) string {
	var repaired strings.Builder
	inString := false

	for i := 0; i < len(input); {
		r, size := utf8.DecodeRuneInString(input[i:])
		if size == 0 {
			break
		}

		if !inString {
			repaired.WriteRune(r)
			if r == '"' {
				inString = true
			}
			i += size
			continue
		}

		if r == '"' {
			repaired.WriteRune(r)
			inString = false
			i += size
			continue
		}

		if r == '\\' {
			if i+size >= len(input) {
				repaired.WriteString("\\\\")
				i += size
				continue
			}

			nextR, nextSize := utf8.DecodeRuneInString(input[i+size:])

			if nextR == 'u' {
				if i+size+nextSize+4 <= len(input) {
					unicodeDigits := input[i+size+nextSize : i+size+nextSize+4]
					if isValidHexDigits(unicodeDigits) {
						repaired.WriteRune(r)
						repaired.WriteRune(nextR)
						repaired.WriteString(unicodeDigits)
						i += size + nextSize + 4
						continue
					}
				}
			}

			if validJSONEscapes[nextR] {
				repaired.WriteRune(r)
				repaired.WriteRune(nextR)
				i += size + nextSize
				continue
			}

			repaired.WriteString("\\\\")
			i += size
			continue
		}

		if isControlCharacter(r) {
			repaired.WriteString(escapeControlCharacter(r))
		} else {
			repaired.WriteRune(r)
		}
		i += size
	}

	return repaired.String()
}

func isValidHexDigits(s string) bool {
	if len(s) != 4 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

func parseJSONWithRepair[T any](input string) (T, error) {
	var result T
	var parseErr error
	if parseErr = json.Unmarshal([]byte(input), &result); parseErr == nil {
		return result, nil
	}

	repaired := repairJSON(input)
	if repaired != input {
		if parseErr = json.Unmarshal([]byte(repaired), &result); parseErr == nil {
			return result, nil
		}
		return result, fmt.Errorf("failed to parse JSON after repair: %w", parseErr)
	}

	return result, fmt.Errorf("failed to parse JSON: %w", parseErr)
}

func parseStreamingJSON(input string) map[string]any {
	if strings.TrimSpace(input) == "" {
		return map[string]any{}
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(input), &result); err == nil {
		return result
	}

	repaired := repairJSON(input)
	if repaired != input {
		if err := json.Unmarshal([]byte(repaired), &result); err == nil {
			return result
		}
	}

	return map[string]any{}
}
