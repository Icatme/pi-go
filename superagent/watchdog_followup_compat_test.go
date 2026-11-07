package superagent

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent/background"
)

func watchdogCheckRecords(t *testing.T, a *SuperAgent) []transition {
	t.Helper()
	var checks []transition
	for _, item := range a.store.Log() {
		if item.Entry == nil || item.Entry.Custom == nil {
			continue
		}
		var tr transition
		if err := json.Unmarshal(item.Entry.Custom.Payload, &tr); err != nil {
			t.Fatal(err)
		}
		if tr.Watchdog != nil && tr.Watchdog.Check != nil {
			checks = append(checks, tr)
		}
	}
	return checks
}

func assertLegacyWatchdogChecks(t *testing.T, a *SuperAgent) {
	t.Helper()
	checks := watchdogCheckRecords(t, a)
	if len(checks) == 0 {
		t.Fatal("fixture did not persist a check")
	}
	for _, tr := range checks {
		if tr.State != "watchdog_checked" || tr.Watchdog.Followups != nil {
			t.Fatal("check without follow-up evidence needlessly changed the old reader contract")
		}
	}
	if a.watchdog.followupVersion {
		t.Fatal("legacy checks enabled the upgrade barrier")
	}
}

func TestWatchdogFollowupEmptyChecksPreserveLegacyEncoding(t *testing.T) {
	a, path := watchdogAgent(t, mainWorker)
	options := watchdogOptions(newWatchdogClock())
	checkWatchdog(t, a, options)
	options.Clock.(*watchdogClock).advance(options.Policy.Interval)
	checkWatchdog(t, a, options)
	assertLegacyWatchdogChecks(t, a)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMain(t.Context(), path, 1, bgMain, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	options.Clock.(*watchdogClock).advance(options.Policy.Interval)
	checkWatchdog(t, b, options)
	assertLegacyWatchdogChecks(t, b)
}

func TestWatchdogFollowupFutureInterventionPreservesLegacyEncoding(t *testing.T) {
	a, _, _, request := refreshFixture(t)
	i, err := a.RefreshWatchdog(t.Context(), request, refreshGrant)
	if err != nil {
		t.Fatal(err)
	}
	options := watchdogOptions(newWatchdogClock())
	options.Clock.(*watchdogClock).advance(options.Policy.Interval)
	checkWatchdog(t, a, options)
	if time.Now().UnixMilli() >= i.RecheckAt {
		t.Fatal("fixture unexpectedly exceeded the intervention's one-minute deadline")
	}
	assertLegacyWatchdogChecks(t, a)
}

func TestWatchdogFollowupPendingInterventionPreservesLegacyEncoding(t *testing.T) {
	a, _, backend, request := refreshFixture(t)
	started := make(chan struct{})
	backend.mu.Lock()
	backend.inspect = func(ctx context.Context, _ string) (background.Observation, error) {
		close(started)
		<-ctx.Done()
		return background.Observation{}, ctx.Err()
	}
	backend.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := a.RefreshWatchdog(ctx, request, refreshGrant)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("intervention did not reach Inspect")
	}
	options := watchdogOptions(newWatchdogClock())
	options.Clock.(*watchdogClock).advance(options.Policy.Interval)
	checkWatchdog(t, a, options)
	assertLegacyWatchdogChecks(t, a)
	if got := a.WatchdogStatus().Interventions; len(got) != 1 || got[0].State != "pending" {
		t.Fatal("fixture did not retain the pending intervention")
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("interrupted Inspect unexpectedly completed")
	}
}

func TestWatchdogFollowupEvidenceUpgradePersistsAcrossEmptyChecksAndRestart(t *testing.T) {
	a, path, backend, request, options := followupFixture(t)
	assertLegacyWatchdogChecks(t, a)
	i, err := a.RefreshWatchdog(t.Context(), request, refreshGrant)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(time.UnixMilli(i.RecheckAt)))
	options.Clock.(*watchdogClock).advance(options.Policy.Interval)
	checkWatchdog(t, a, options)
	options.Clock.(*watchdogClock).advance(options.Policy.Interval)
	checkWatchdog(t, a, options)
	checks := watchdogCheckRecords(t, a)
	if len(checks) != 3 || checks[1].State != "watchdog_checked_followups" || len(checks[1].Watchdog.Followups.Followups) != 1 || checks[2].State != "watchdog_checked_followups" || checks[2].Watchdog.Followups == nil || len(checks[2].Watchdog.Followups.Followups) != 0 {
		t.Fatal("evidence upgrade or subsequent empty-batch barrier was lost")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMainBackground(t.Context(), path, 1, bgMain, mainWorker, refreshOptions(backend))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if !b.watchdog.followupVersion || len(b.WatchdogFollowupsSince(0, 32).Followups) != 1 {
		t.Fatal("restart lost the evidence upgrade")
	}
	options.Clock.(*watchdogClock).advance(options.Policy.Interval)
	checkWatchdog(t, b, options)
	checks = watchdogCheckRecords(t, b)
	last := checks[len(checks)-1]
	if last.State != "watchdog_checked_followups" || last.Watchdog.Followups == nil {
		t.Fatal("restart downgraded an upgraded journal")
	}
	last.State = "watchdog_checked"
	last.Watchdog.Followups = nil
	if err := b.validateWatchdogFollowups(last); err == nil {
		t.Fatal("upgraded journal accepted a later old-writer check")
	}
}

func TestWatchdogFollowupPreviouslyUpgradedEmptyJournalStaysUpgraded(t *testing.T) {
	a, path := watchdogAgent(t, mainWorker)
	options := watchdogOptions(newWatchdogClock())
	checkWatchdog(t, a, options)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Earlier builds wrote the new state even before any intervention existed.
	// Keep reading those journals and preserve their existing upgrade barrier.
	raw = rewriteFollowupLog(t, raw, func(tr *transition) {
		if tr.State == "watchdog_checked" {
			tr.State = "watchdog_checked_followups"
			tr.Watchdog.Followups = &watchdogFollowupBatch{ObservedAt: time.Now().UnixMilli()}
		}
	})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMain(t.Context(), path, 1, bgMain, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if !b.watchdog.followupVersion {
		t.Fatal("existing empty upgrade marker was discarded")
	}
	options.Clock.(*watchdogClock).advance(options.Policy.Interval)
	checkWatchdog(t, b, options)
	checks := watchdogCheckRecords(t, b)
	if checks[len(checks)-1].State != "watchdog_checked_followups" {
		t.Fatal("existing empty upgrade marker was downgraded")
	}
}
