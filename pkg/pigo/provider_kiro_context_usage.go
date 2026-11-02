package pigo

// kiroOfflineContextUsage validates the observed standalone event schema.
// It deliberately uses the metadata collector's strict decimal subset, but
// never changes that collector: pressure observations are not final token usage.
func kiroOfflineContextUsage(payload []byte, maxBytes int) (float64, error) {
	fields, err := decodeKiroJSONObject(payload, maxBytes)
	if err != nil || len(fields) != 1 {
		return 0, errKiroOfflineResponse
	}
	raw, found := fields["contextUsagePercentage"]
	if !found {
		return 0, errKiroOfflineResponse
	}
	value, valid := kiroMetadataFraction(raw, true)
	if !valid {
		return 0, errKiroOfflineResponse
	}
	return value, nil
}
