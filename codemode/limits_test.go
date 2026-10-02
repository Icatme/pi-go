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
	if limits, err := sandbox.SourceLimits("// @options: {\"max_output_tokens\":0}\ntext(1);", RunOptions{}); err == nil || limits != (SourceLimits{}) {
		t.Fatalf("invalid options returned usable limits: %+v err=%v", limits, err)
	}
}
