package superagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent/session"
)

// Alter only transition payloads, retaining the original valid session chain.
func rewriteFollowupLog(t *testing.T, raw []byte, change func(*transition)) []byte {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
	for n := 1; n < len(lines); n++ {
		var item session.LogItem
		if err := json.Unmarshal(lines[n], &item); err != nil {
			t.Fatal(err)
		}
		var tr transition
		if err := json.Unmarshal(item.Entry.Custom.Payload, &tr); err != nil {
			t.Fatal(err)
		}
		change(&tr)
		var err error
		item.Entry.Custom.Payload, err = json.Marshal(tr)
		if err != nil {
			t.Fatal(err)
		}
		lines[n], err = json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
	}
	return append(bytes.Join(lines, []byte{'\n'}), '\n')
}

func TestWatchdogFollowupReplayRejectsForgedEvidence(t *testing.T) {
	a, path, _, request, options := followupFixture(t)
	intervention, err := a.RefreshWatchdog(t.Context(), request, refreshGrant)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(time.UnixMilli(intervention.RecheckAt)))
	options.Clock.(*watchdogClock).advance(time.Millisecond)
	checkWatchdog(t, a, options)
	original := a.WatchdogFollowupsSince(0, 1).Followups[0]
	options.Clock.(*watchdogClock).advance(time.Millisecond)
	lastCheck := checkWatchdog(t, a, options)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*transition){
		"missing-batch":    func(tr *transition) { tr.Watchdog.Followups = nil },
		"dropped-evidence": func(tr *transition) { tr.Watchdog.Followups.Followups = nil },
		"duplicate-in-batch": func(tr *transition) {
			tr.Watchdog.Followups.Followups = append(tr.Watchdog.Followups.Followups, original)
		},
		"premature-wall-time": func(tr *transition) {
			tr.Watchdog.Followups.ObservedAt = intervention.RecheckAt - 1
			tr.Watchdog.Followups.Followups[0].ObservedAt = intervention.RecheckAt - 1
		},
		"wrong-origin":          func(tr *transition) { tr.Watchdog.Followups.Followups[0].Request.Origin.EventID = "other" },
		"wrong-operation":       func(tr *transition) { tr.Watchdog.Followups.Followups[0].Operation.ID = "other" },
		"wrong-sequence":        func(tr *transition) { tr.Watchdog.Followups.Followups[0].Sequence++ },
		"wrong-id":              func(tr *transition) { tr.Watchdog.Followups.Followups[0].ID = "other" },
		"wrong-source":          func(tr *transition) { tr.Watchdog.Followups.Followups[0].Source = "backend_inspected" },
		"invented-success":      func(tr *transition) { tr.Watchdog.Followups.Followups[0].Class = "resolved" },
		"cleared-review":        func(tr *transition) { tr.Watchdog.Followups.Followups[0].Operation.NeedsReview = false },
		"forged-task":           func(tr *transition) { tr.Watchdog.Followups.Followups[0].Task.Pending++ },
		"forged-report":         func(tr *transition) { tr.Watchdog.Followups.Followups[0].Task.LastReport = "invented" },
		"wrong-check-cursor":    func(tr *transition) { tr.Watchdog.Check.Cursor = 999 },
		"mixed-business-action": func(tr *transition) { tr.Main = &mainTransition{} },
		"mixed-operation":       func(tr *transition) { tr.Watchdog.Operation = &BackgroundOperation{} },
		"downgraded-check": func(tr *transition) {
			tr.State = "watchdog_checked"
			tr.Watchdog.Followups = nil
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := false
			bad := rewriteFollowupLog(t, raw, func(tr *transition) {
				if !changed && tr.Watchdog != nil && tr.Watchdog.Followups != nil && len(tr.Watchdog.Followups.Followups) != 0 {
					mutate(tr)
					changed = true
				}
			})
			if !changed {
				t.Fatal("fixture lacked follow-up record")
			}
			assertFollowupReplayRejected(t, bad)
		})
	}
	t.Run("duplicate-across-checks", func(t *testing.T) {
		bad := rewriteFollowupLog(t, raw, func(tr *transition) {
			if tr.Watchdog != nil && tr.Watchdog.Check != nil && tr.Watchdog.Check.ID == lastCheck.ID {
				duplicate := original
				duplicate.Sequence = 2
				duplicate.ObservedAt = tr.Watchdog.Followups.ObservedAt
				tr.Watchdog.Followups.Followups = []WatchdogFollowup{duplicate}
			}
		})
		assertFollowupReplayRejected(t, bad)
	})
}

func assertFollowupReplayRejected(t *testing.T, raw []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "forged.jsonl")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	// Rejection must be semantic, not an accidentally broken outer log chain.
	store, err := session.OpenJSONLStorageStrict(path)
	if err != nil {
		t.Fatalf("mutant broke session structure: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	backend := &refreshBackend{}
	b, err := OpenMainBackground(t.Context(), path, 1, bgMain, mainWorker, refreshOptions(backend))
	if err == nil {
		b.Close()
		t.Fatal("forged follow-up replayed")
	}
	starts, stops, inspections := backend.counts()
	if starts+stops+inspections != 0 {
		t.Fatal("invalid replay reached a backend")
	}
}

func TestWatchdogFollowupLegacyChecksUpgradeWithoutReissuingRefresh(t *testing.T) {
	a, path, _, request, options := followupFixture(t)
	i, err := a.RefreshWatchdog(t.Context(), request, refreshGrant)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy := rewriteFollowupLog(t, raw, func(tr *transition) {
		if tr.State == "watchdog_checked_followups" {
			if len(tr.Watchdog.Followups.Followups) != 0 {
				t.Fatal("fixture already followed up")
			}
			tr.State = "watchdog_checked"
			tr.Watchdog.Followups = nil
		}
	})
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	backend := &refreshBackend{}
	b, err := OpenMainBackground(t.Context(), path, 1, bgMain, mainWorker, refreshOptions(backend))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if b.WatchdogStatus().Interventions[0] != i || len(b.WatchdogFollowupsSince(0, 1).Followups) != 0 {
		t.Fatal("upgrade changed or resumed intervention")
	}
	time.Sleep(time.Until(time.UnixMilli(i.RecheckAt)))
	options.Clock.(*watchdogClock).advance(time.Millisecond)
	checkWatchdog(t, b, options)
	if got := b.WatchdogFollowupsSince(0, 1); len(got.Followups) != 1 || got.Followups[0].Request != request {
		t.Fatalf("legacy upgrade lost due evidence: %+v", got)
	}
}

type followupUncertainStore struct{ Journal }

func (s followupUncertainStore) AppendEntry(lane string, entry session.NewEntry) (session.Entry, error) {
	result, err := s.Journal.AppendEntry(lane, entry)
	if err == nil {
		var tr transition
		if entry.Custom != nil && json.Unmarshal(entry.Custom.Payload, &tr) == nil && tr.State == "watchdog_checked_followups" {
			return session.Entry{}, errors.New("fixture acknowledgement lost after durable append")
		}
	}
	return result, err
}

func TestWatchdogFollowupUnknownCommitRecoversExactlyOnce(t *testing.T) {
	a, path, backend, request, options := followupFixture(t)
	i, err := a.RefreshWatchdog(t.Context(), request, refreshGrant)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(time.UnixMilli(i.RecheckAt)))
	before := a.WatchdogStatus()
	a.mu.Lock()
	a.store = followupUncertainStore{a.store}
	a.mu.Unlock()
	options.Clock.(*watchdogClock).advance(time.Millisecond)
	if _, err := a.CheckWatchdog(t.Context(), options); err == nil {
		t.Fatal("uncertain append reported success")
	}
	if !reflect.DeepEqual(before, a.WatchdogStatus()) || len(a.WatchdogFollowupsSince(0, 32).Followups) != 0 {
		t.Fatal("uncertain commit published live evidence")
	}
	if err := a.Close(); err == nil {
		t.Fatal("unknown commit did not fail-stop")
	}
	b, err := OpenMainBackground(t.Context(), path, 1, bgMain, mainWorker, refreshOptions(backend))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	page := b.WatchdogFollowupsSince(0, 32)
	if len(page.Followups) != 1 || page.Followups[0].Request != request || len(b.WatchdogStatus().Checks) != len(before.Checks)+1 {
		t.Fatal("durable atomic check/follow-up lost on reopen")
	}
	options.Clock.(*watchdogClock).advance(time.Millisecond)
	checkWatchdog(t, b, options)
	if got := b.WatchdogFollowupsSince(0, 32); !reflect.DeepEqual(got, page) {
		t.Fatal("unknown commit replay duplicated evidence")
	}
}
