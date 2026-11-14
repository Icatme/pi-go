package pigo

// nonnegativeTokenSumExceeds compares token components without overflowing an
// intermediate sum. Negative counts are not valid evidence for a threshold.
// This does not validate or repair usage for accounting purposes.
func nonnegativeTokenSumExceeds(limit int, counts ...int) bool {
	for _, count := range counts {
		if count < 0 {
			return false
		}
	}
	if limit < 0 {
		return true
	}
	remaining := limit
	for _, count := range counts {
		if count > remaining {
			return true
		}
		remaining -= count
	}
	return false
}
