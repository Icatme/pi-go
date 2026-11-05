package schedulereval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

type StrategySummary struct {
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Limitation  string    `json:"limitation"`
	Repetitions []Metrics `json:"repetitions"`
}
type BacklogSummary struct {
	RatePerMinute       int     `json:"rate_per_minute"`
	MaxBatch            int     `json:"max_batch"`
	Repetition          int     `json:"repetition"`
	Items               int     `json:"items"`
	Batches             int     `json:"batches"`
	QueueP95MS          *int64  `json:"queue_wait_p95_ms"`
	DecisionP95MS       *int64  `json:"amortized_decision_p95_ms"`
	ThroughputPerMinute float64 `json:"throughput_per_minute"`
	Errors              int     `json:"errors"`
}
type Summary struct {
	Schema             string            `json:"schema"`
	Evidence           string            `json:"evidence"`
	BaseCommit         string            `json:"base_commit"`
	WorkloadDigest     string            `json:"workload_sha256"`
	FullResultDigest   string            `json:"canonical_full_result_sha256"`
	HistoricalBaseline string            `json:"pre_m1_baseline_status"`
	Budget             Budget            `json:"budget"`
	Strategies         []StrategySummary `json:"strategies"`
	Backlog            []BacklogSummary  `json:"router_backlog"`
}

// Summarize keeps the checked-in baseline compact. The digest covers json.Marshal
// of the complete Suite, including every event/task/batch and all five repeats.
func Summarize(s Suite) (Summary, error) {
	out := Summary{Schema: s.Schema, Evidence: "synthetic virtual-time results only; run without -summary to retain all per-event records", BaseCommit: s.BaseCommit, WorkloadDigest: s.WorkloadDigest, HistoricalBaseline: s.HistoricalBaseline, Budget: s.Budget}
	raw, err := json.Marshal(s)
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(raw)
	out.FullResultDigest = hex.EncodeToString(sum[:])
	for _, st := range s.Strategies {
		v := StrategySummary{Name: st.Name, Status: st.Status, Limitation: st.Limitation}
		for _, r := range st.Runs {
			v.Repetitions = append(v.Repetitions, r.Metrics)
		}
		out.Strategies = append(out.Strategies, v)
	}
	for _, r := range s.Backlog {
		out.Backlog = append(out.Backlog, BacklogSummary{RatePerMinute: r.RatePerMinute, MaxBatch: r.MaxBatch, Repetition: r.Repetition, Items: len(r.Items), Batches: len(r.Batches), QueueP95MS: r.QueueP95MS, DecisionP95MS: r.DecisionP95MS, ThroughputPerMinute: r.ThroughputPerMinute, Errors: r.Errors})
	}
	return out, nil
}
