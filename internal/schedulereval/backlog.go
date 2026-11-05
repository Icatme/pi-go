package schedulereval

import (
	"encoding/json"
	"fmt"
)

type BacklogItem struct {
	ID        string `json:"id"`
	ArrivedMS int64  `json:"arrived_ms"`
	StartedMS int64  `json:"started_ms"`
	SettledMS int64  `json:"settled_ms"`
}
type BacklogRun struct {
	Repetition          int           `json:"repetition"`
	Status              string        `json:"status"`
	RatePerMinute       int           `json:"rate_per_minute"`
	MaxBatch            int           `json:"max_batch"`
	RouterBaseMS        int64         `json:"router_base_ms"`
	RouterItemMS        int64         `json:"router_item_ms"`
	Items               []BacklogItem `json:"items"`
	Batches             []Batch       `json:"batches"`
	QueueP50MS          *int64        `json:"queue_wait_p50_ms"`
	QueueP95MS          *int64        `json:"queue_wait_p95_ms"`
	DecisionP50MS       *int64        `json:"amortized_decision_p50_ms"`
	DecisionP95MS       *int64        `json:"amortized_decision_p95_ms"`
	ThroughputPerMinute float64       `json:"throughput_per_minute"`
	Errors              int           `json:"errors"`
}

// backlog models a single serial Router server, with fixed base + per-item
// service time. Rates and timing are inputs, never measured inference costs.
func backlog(rate, maxBatch int) (BacklogRun, error) {
	r := BacklogRun{Status: "simulated", RatePerMinute: rate, MaxBatch: maxBatch, RouterBaseMS: 2000, RouterItemMS: 200}
	if rate < 1 || rate > 60000 || maxBatch < 1 || maxBatch > 64 {
		return r, fmt.Errorf("invalid backlog configuration")
	}
	const count = 300
	for i := 0; i < count; i++ {
		r.Items = append(r.Items, BacklogItem{ID: fmt.Sprintf("cold-%03d", i+1), ArrivedMS: int64(i) * 60000 / int64(rate)})
	}
	var now int64
	var waits, decisions []int64
	for next := 0; next < count; {
		if now < r.Items[next].ArrivedMS {
			now = r.Items[next].ArrivedMS
		}
		end := next + 1
		for end < count && end-next < maxBatch && r.Items[end].ArrivedMS <= now {
			end++
		}
		duration := r.RouterBaseMS + r.RouterItemMS*int64(end-next)
		batch := Batch{StartedMS: now, FinishedMS: now + duration}
		for i := next; i < end; i++ {
			item := &r.Items[i]
			item.StartedMS = now
			item.SettledMS = now + duration
			wait := now - item.ArrivedMS
			waits = append(waits, wait)
			decisions = append(decisions, duration/int64(end-next))
			batch.Keys = append(batch.Keys, item.ID)
			batch.QueueWaitMS = append(batch.QueueWaitMS, wait)
		}
		encoded, err := json.Marshal(r.Items[next:end])
		if err != nil {
			return r, err
		}
		batch.InputBytes = len(encoded)
		r.Batches = append(r.Batches, batch)
		now += duration
		next = end
	}
	r.QueueP50MS = quantile(waits, 50)
	r.QueueP95MS = quantile(waits, 95)
	r.DecisionP50MS = quantile(decisions, 50)
	r.DecisionP95MS = quantile(decisions, 95)
	r.ThroughputPerMinute = float64(count) * 60000 / float64(now)
	return r, nil
}
