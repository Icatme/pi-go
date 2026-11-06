// Package schedulereval supplies an explicitly synthetic, offline scheduling
// experiment. It is not a model benchmark or another production scheduler.
package schedulereval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
)

const BaseCommit = "89d8c23c1fde5df588f84a23f5b6f5ce52ce6e18"

type Event struct {
	Sequence int    `json:"sequence"`
	AtMS     int64  `json:"at_ms"`
	Source   string `json:"source"`
	ID       string `json:"id"`
	Thread   string `json:"thread"`
	Kind     string `json:"kind"`
	WorkID   string `json:"work_id,omitempty"`
	Text     string `json:"text"`
}

func (e Event) key() string { return e.Source + "/" + e.ID }

type Task struct {
	ID     string `json:"id"`
	ToolMS int64  `json:"tool_ms"`
	Cancel bool   `json:"expect_cancel"`
}

type Expected struct {
	Action string `json:"action"`
	TaskID string `json:"task_id,omitempty"`
}

type Workload struct {
	Version string              `json:"version"`
	Tasks   []Task              `json:"tasks"`
	Events  []Event             `json:"events"`
	Oracle  map[string]Expected `json:"oracle"`
}

// Fixture is fixed, with no random source or wall clock. Each of twenty topics
// contributes one request, two updates, one control/noise event, ten noise
// events, and a duplicate callback. Five topics share each chat.
func Fixture() Workload {
	w := Workload{Version: "m9a-v1", Oracle: map[string]Expected{}}
	add := func(e Event, want Expected) { w.Events = append(w.Events, e); w.Oracle[e.key()] = want }
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("work-%02d", i+1)
		base := int64(i) * 1000
		thread := fmt.Sprintf("chat-%d", i/5+1)
		w.Tasks = append(w.Tasks, Task{ID: id, ToolMS: 120000 + int64(i%3)*10000, Cancel: i%5 == 0})
		event := func(kind string, at int64) Event {
			return Event{AtMS: base + at, Source: "im", ID: id + "-" + kind, Thread: thread, Kind: kind, WorkID: id, Text: kind + " for " + id}
		}
		add(event("request", 0), Expected{Action: "new", TaskID: id})
		add(event("followup", 20000), Expected{Action: "attach", TaskID: id})
		cb := event("callback", 40000)
		cb.Source = "github"
		add(cb, Expected{Action: "attach", TaskID: id})
		if i%5 == 0 {
			add(event("cancel", 60000), Expected{Action: "cancel", TaskID: id})
		} else {
			e := event("noise", 60000)
			e.WorkID = ""
			add(e, Expected{Action: "ignore"})
		}
		for j := 0; j < 10; j++ {
			e := event("noise", int64(j)*7000+500)
			e.ID = fmt.Sprintf("%s-noise-%d", id, j)
			e.WorkID = ""
			add(e, Expected{Action: "ignore"})
		}
		cb.AtMS += 3000
		w.Events = append(w.Events, cb)
	}
	slices.SortStableFunc(w.Events, func(a, b Event) int {
		if a.AtMS < b.AtMS {
			return -1
		}
		if a.AtMS > b.AtMS {
			return 1
		}
		return 0
	})
	for i := range w.Events {
		w.Events[i].Sequence = i + 1
	}
	return w
}
func (w Workload) Digest() string {
	b, _ := json.Marshal(w)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func (w Workload) Validate() error {
	tasks := map[string]bool{}
	requests := map[string]int{}
	seen := map[string]Event{}
	for _, t := range w.Tasks {
		if t.ID == "" || tasks[t.ID] || t.ToolMS <= 0 {
			return fmt.Errorf("invalid task %q", t.ID)
		}
		tasks[t.ID] = true
	}
	for i, e := range w.Events {
		if e.Sequence != i+1 || e.AtMS < 0 || e.Source == "" || e.ID == "" || e.Thread == "" || (i > 0 && e.AtMS < w.Events[i-1].AtMS) {
			return fmt.Errorf("invalid event at %d", i)
		}
		if old, ok := seen[e.key()]; ok {
			old.AtMS = e.AtMS
			old.Sequence = e.Sequence
			if old != e {
				return fmt.Errorf("conflicting duplicate %s", e.key())
			}
			continue
		}
		seen[e.key()] = e
		want, ok := w.Oracle[e.key()]
		if !ok {
			return fmt.Errorf("missing oracle %s", e.key())
		}
		if e.Kind != "noise" && !tasks[e.WorkID] {
			return fmt.Errorf("unknown work %s", e.WorkID)
		}
		switch e.Kind {
		case "request":
			requests[e.WorkID]++
		case "followup", "callback", "cancel", "noise":
		default:
			return fmt.Errorf("unknown event kind %s", e.Kind)
		}
		switch want.Action {
		case "new", "attach", "cancel":
			if !tasks[want.TaskID] {
				return fmt.Errorf("invalid expected task %s", want.TaskID)
			}
		case "ignore":
			if want.TaskID != "" {
				return fmt.Errorf("ignore has task")
			}
		default:
			return fmt.Errorf("invalid expected action")
		}
	}
	if len(seen) != len(w.Oracle) {
		return fmt.Errorf("orphan oracle entries")
	}
	for id := range tasks {
		if requests[id] != 1 {
			return fmt.Errorf("work %s needs exactly one request", id)
		}
	}
	return nil
}
