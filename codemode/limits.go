package codemode

import "time"

// SourceLimits validates execution options without starting a VM. Adapters can
// apply the same deadline to lazy directory setup and the subsequent script.
func (s *Sandbox) SourceLimits(code string, options RunOptions) (time.Duration, int, error) {
	_, timeout, tokens, err := sourceOptions(code, s.config, options)
	return timeout, tokens, err
}
