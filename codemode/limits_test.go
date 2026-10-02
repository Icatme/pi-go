package codemode

import (
	"testing"
	"time"
)

func TestSourceLimitsIncludeConfiguredBytesAndHeader(t *testing.T) {
	config := DefaultConfig()
	config.Timeout, config.MaxOutputBytes = time.Second, 17
	sandbox := sandboxForTest(t, config)
	for _, test := range []struct {
		name    string
		source  string
		options RunOptions
		want    SourceLimits
	}{
		{"configured_bytes", "text(1);", RunOptions{}, SourceLimits{Timeout: time.Second, OutputBytes: 17}},
		{"option_tokens", "text(1);", RunOptions{MaxOutputTokens: 4}, SourceLimits{Timeout: time.Second, OutputBytes: 16}},
		{"header_tokens", "// @options: {\"max_output_tokens\":1}\ntext(1);", RunOptions{}, SourceLimits{Timeout: time.Second, OutputBytes: 4}},
		{"header_timeout", "// @options: {\"timeout_ms\":250}\ntext(1);", RunOptions{Timeout: time.Second / 2}, SourceLimits{Timeout: time.Second / 4, OutputBytes: 17}},
	} {
		t.Run(test.name, func(t *testing.T) {
			limits, err := sandbox.SourceLimits(test.source, test.options)
			if err != nil || limits != test.want {
				t.Fatalf("limits=%+v want=%+v err=%v", limits, test.want, err)
			}
		})
	}
}

func TestInvalidSourceLimitsRetainOnlyDiagnosticBudget(t *testing.T) {
	config := DefaultConfig()
	config.MaxOutputBytes = 8
	config.MaxCodeBytes = 128
	sandbox := sandboxForTest(t, config)
	for _, test := range []struct {
		name, source string
		options      RunOptions
		want         int
	}{
		{"invalid_timeout", "// @options: {\"timeout_ms\":-1}\ntext(1);", RunOptions{}, 8},
		{"invalid_tokens", "// @options: {\"max_output_tokens\":0}\ntext(1);", RunOptions{MaxOutputTokens: 1}, 4},
		{"malformed_header", "// @options: {", RunOptions{}, 8},
		{"oversized_source", string(make([]byte, 129)), RunOptions{}, 8},
		{"invalid_host_options", "text(1);", RunOptions{Timeout: -1, MaxOutputTokens: 1}, 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			limits, err := sandbox.SourceLimits(test.source, test.options)
			if err == nil || limits.Timeout != 0 || limits.OutputBytes != test.want {
				t.Fatalf("invalid source lost diagnostic-only bounds: limits=%+v err=%v", limits, err)
			}
			result, runErr := sandbox.Run(t.Context(), test.source, test.options)
			if runErr == nil || result.OutputLimitBytes != limits.OutputBytes || len(result.Calls) != 0 {
				t.Fatalf("pre-VM limits differ from execution: result=%+v err=%v", result, runErr)
			}
		})
	}
}
