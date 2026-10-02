package codemode

import "time"

// SourceLimits contains the effective host/source bounds before VM creation.
// OutputBytes includes both the configured byte ceiling and the token budget.
type SourceLimits struct {
	Timeout     time.Duration
	OutputBytes int
}

// SourceLimits validates execution options without starting a VM. Adapters can
// apply the same deadline to lazy directory setup and the subsequent script.
// On error, only OutputBytes is set, to the conservative host/RunOptions bound
// for diagnostics. The error still prohibits directory setup or execution.
func (s *Sandbox) SourceLimits(code string, options RunOptions) (SourceLimits, error) {
	_, timeout, tokens, err := sourceOptions(code, s.config, options)
	if err != nil {
		return SourceLimits{OutputBytes: s.outputLimitBytes(options)}, err
	}
	return SourceLimits{Timeout: timeout, OutputBytes: min(s.config.MaxOutputBytes, tokens*4)}, nil
}

func (s *Sandbox) outputLimitBytes(options RunOptions) int {
	tokens := s.config.MaxOutputTokens
	if options.MaxOutputTokens > 0 {
		tokens = min(tokens, options.MaxOutputTokens)
	}
	return min(s.config.MaxOutputBytes, tokens*4)
}
