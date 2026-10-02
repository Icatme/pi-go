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
func (s *Sandbox) SourceLimits(code string, options RunOptions) (SourceLimits, error) {
	_, timeout, tokens, err := sourceOptions(code, s.config, options)
	if err != nil {
		return SourceLimits{}, err
	}
	return SourceLimits{Timeout: timeout, OutputBytes: min(s.config.MaxOutputBytes, tokens*4)}, nil
}
