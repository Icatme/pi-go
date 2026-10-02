package codemode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func sourceOptions(source string, config Config, opts RunOptions) (string, time.Duration, int, error) {
	if len(source) > config.MaxCodeBytes {
		return "", 0, 0, fmt.Errorf("source exceeds byte limit")
	}
	timeout := config.Timeout
	tokens := config.MaxOutputTokens
	if opts.Timeout < 0 || opts.MaxOutputTokens < 0 {
		return "", 0, 0, fmt.Errorf("invalid Run options")
	}
	if opts.Timeout > 0 {
		if timeout > 0 && opts.Timeout > timeout {
			return "", 0, 0, fmt.Errorf("Run timeout cannot increase host limit")
		}
		timeout = opts.Timeout
	}
	if opts.MaxOutputTokens > 0 {
		if opts.MaxOutputTokens > tokens {
			return "", 0, 0, fmt.Errorf("output tokens cannot increase host limit")
		}
		tokens = opts.MaxOutputTokens
	}
	line, _, _ := strings.Cut(source, "\n")
	if strings.HasPrefix(line, "// @options:") {
		var fields map[string]json.RawMessage
		rawOptions := strings.TrimSpace(strings.TrimPrefix(line, "// @options:"))
		if !json.Valid([]byte(rawOptions)) {
			return "", 0, 0, fmt.Errorf("invalid source options")
		}
		d := json.NewDecoder(bytes.NewBufferString(rawOptions))
		if err := d.Decode(&fields); err != nil || fields == nil {
			return "", 0, 0, fmt.Errorf("invalid source options")
		}
		// Require one whole object, not a valid prefix followed by junk.
		if d.More() {
			return "", 0, 0, fmt.Errorf("invalid source options")
		}
		for name, value := range fields {
			var n int64
			if err := json.Unmarshal(value, &n); err != nil || n <= 0 {
				return "", 0, 0, fmt.Errorf("invalid option %s", name)
			}
			switch name {
			case "timeout_ms":
				if (timeout > 0 && n > int64(timeout/time.Millisecond)) || n > int64(24*time.Hour/time.Millisecond) {
					return "", 0, 0, fmt.Errorf("source timeout cannot increase host limit")
				}
				timeout = time.Duration(n) * time.Millisecond
			case "max_output_tokens":
				if n > int64(tokens) {
					return "", 0, 0, fmt.Errorf("source output tokens cannot increase host limit")
				}
				tokens = int(n)
			default:
				return "", 0, 0, fmt.Errorf("unknown source option %s", name)
			}
		}
		// Keeping its line preserves user source line numbers in codemode.js.
		source = strings.Repeat(" ", len(line)) + source[len(line):]
	}
	return source, timeout, tokens, nil
}
