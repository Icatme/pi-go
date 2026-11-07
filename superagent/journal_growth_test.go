package superagent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/session"
)

// Fixtures exercise real coordinator commits without paying for an fsync on
// every setup transition. Serialization retains the header and every LogItem;
// this is a test fixture writer, not a production export/checkpoint API.
func writeGrowthJournal(tb testing.TB, store Journal) (string, int, int64) {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), "journal.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		tb.Fatal(err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	encoder := json.NewEncoder(w)
	if err := encoder.Encode(store.Header()); err != nil {
		tb.Fatal(err)
	}
	log := store.Log()
	for _, item := range log {
		if err := encoder.Encode(item); err != nil {
			tb.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		tb.Fatal(err)
	}
	if err := f.Close(); err != nil {
		tb.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		tb.Fatal(err)
	}
	return path, len(log), info.Size()
}

func growthMemory(tb testing.TB, ctx context.Context) *testLeasedJournal {
	tb.Helper()
	store, err := session.NewMemoryStorage(session.Header{ID: "superagent"})
	if err != nil {
		tb.Fatal(err)
	}
	lifetime, cancel := context.WithCancel(ctx)
	tb.Cleanup(cancel)
	return &testLeasedJournal{MemoryStorage: store, ctx: lifetime, cancel: cancel}
}

func growthFixture(tb testing.TB, count int, growing bool) (string, int, int64) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	store := growthMemory(tb, ctx)
	route := func(_ context.Context, job MainJob) (MainOutcome, error) {
		snapshot := job.Snapshot
		if growing {
			snapshot.Messages = append(snapshot.Messages, agent.NewUserTextMessage(strings.Repeat("g", 256)))
		} else {
			snapshot.Messages = []agent.Message{agent.NewUserTextMessage(strings.Repeat("f", 4<<10))}
		}
		return MainOutcome{Snapshot: snapshot, Decision: MainDecision{Action: "reply", Text: "public reply"}}, nil
	}
	a, err := OpenMainJournal(ctx, store, MainJournalOptions{Parallel: 1, Router: route, Worker: mainWorker})
	if err != nil {
		tb.Fatal(err)
	}
	defer a.Close()
	for i := 0; i < count; i++ {
		trigger := Trigger{ID: fmt.Sprintf("event-%d", i), Source: "im", Thread: "chat", Kind: "request", Text: "public request"}
		if accepted, err := a.Submit(trigger); !accepted || err != nil {
			tb.Fatalf("submit %d: accepted=%v err=%v", i, accepted, err)
		}
		if err := a.WaitIdle(ctx); err != nil {
			tb.Fatal(err)
		}
	}
	if len(a.Replies()) != count {
		tb.Fatal("fixture did not settle every input")
	}
	if err := a.Close(); err != nil {
		tb.Fatal(err)
	}
	return writeGrowthJournal(tb, store)
}

// Each phase excludes fixture generation and its cleanup. These are warm-cache
// local baselines, not cold-disk latency or peak/retained-memory measurements.
// Restore includes Journal.Log's detached copy, as production restore does.
func BenchmarkJournalGrowth(b *testing.B) {
	for _, profile := range []string{"fixed4KiB", "growing256B"} {
		for _, count := range []int{100, 1000} {
			b.Run(fmt.Sprintf("%s/inputs=%d", profile, count), func(b *testing.B) {
				path, entries, bytes := growthFixture(b, count, profile == "growing256B")
				metrics := func(b *testing.B) {
					b.ReportAllocs()
					b.ReportMetric(float64(entries), "entries")
					b.ReportMetric(float64(bytes), "journal-B")
					b.ReportMetric(float64(bytes)/float64(count), "journal-B/input")
				}
				b.Run("OpenJSONL", func(b *testing.B) {
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						store, err := session.OpenJSONLStorageStrict(path)
						b.StopTimer()
						if err != nil {
							b.Fatal(err)
						}
						if err := store.Close(); err != nil {
							b.Fatal(err)
						}
						b.StartTimer()
					}
					b.StopTimer()
					metrics(b)
				})
				for _, phase := range []string{"Log", "Restore"} {
					b.Run(phase, func(b *testing.B) {
						store, err := session.OpenJSONLStorageStrict(path)
						if err != nil {
							b.Fatal(err)
						}
						defer store.Close()
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							if phase == "Log" {
								if got := len(store.Log()); got != entries {
									b.Fatalf("log length=%d, want %d", got, entries)
								}
							} else {
								a, err := restoreState(context.Background(), store)
								b.StopTimer()
								if err != nil {
									b.Fatal(err)
								}
								// No scheduler was started. Cancel this restore without
								// closing the shared read fixture between iterations.
								a.cancel()
								if a.sequence != entries || len(a.events) != count || len(a.Replies()) != count {
									b.Fatal("replay lost committed fixture state")
								}
								b.StartTimer()
							}
						}
						b.StopTimer()
						metrics(b)
					})
				}
			})
		}
	}
}

func TestJournalGrowthReplayIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	store := growthMemory(t, ctx)
	var callbacks atomic.Int32
	var ref HistoryRef
	route := func(ctx context.Context, job MainJob) (MainOutcome, error) {
		callbacks.Add(1)
		if job.Input.Kind == "user" {
			for _, candidate := range job.Input.HistoryRefs {
				if candidate.Kind == "trigger" {
					ref = candidate
				}
			}
		}
		out, err := mainFixture(ctx, job)
		out.Snapshot.Messages = []agent.Message{agent.NewUserTextMessage("PRIVATE_MAIN_GROWTH")}
		return out, err
	}
	worker := func(_ context.Context, job Job) (Outcome, error) {
		callbacks.Add(1)
		return Outcome{Summary: "public result", Snapshot: agent.AgentSnapshot{SessionID: job.TaskID, Messages: []agent.Message{agent.NewUserTextMessage("PRIVATE_WORKER_GROWTH")}}}, nil
	}
	a, err := OpenMainJournal(ctx, store, MainJournalOptions{Parallel: 1, Router: route, Worker: worker})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	trigger := Trigger{ID: "original", Source: "im", Thread: "chat", Kind: "request", Text: "public request"}
	submit(t, a, trigger)
	waitIdle(t, a)
	a.mu.Lock()
	history := a.newMainHistory()
	a.mu.Unlock()
	if ref.Domain != a.main.id || ref.ID == "" {
		t.Fatal("router did not receive the public trigger reference")
	}
	wantData, wantLinks := historyReadAll(t, history, ref, 4096, 32)
	history.close()
	wantReplies, wantReports := a.Replies(), a.ReportsSince(0)
	wantSnapshot, _, err := a.MainSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(wantReplies) != 1 || wantReplies[0].Status != "pending" || len(wantReports) != 1 {
		t.Fatal("fixture requires one pending reply and one worker report")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	path, entries, _ := writeGrowthJournal(t, store)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := session.OpenJSONLStorageStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	if !reflect.DeepEqual(loaded.Header(), store.Header()) || !reflect.DeepEqual(loaded.Log(), store.Log()) {
		t.Fatal("fixture serialization changed committed identities or payloads")
	}
	wantCalls := callbacks.Load()
	restored, err := restoreState(ctx, loaded)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if restored.sequence != entries || restored.main.id != a.main.id || restored.main.providerID != a.main.providerID || !reflect.DeepEqual(restored.Replies(), wantReplies) || !reflect.DeepEqual(restored.ReportsSince(0), wantReports) {
		t.Fatal("restore changed runtime, delivery, report or reply identity")
	}
	gotSnapshot, _, err := restored.MainSnapshot()
	if err != nil || !reflect.DeepEqual(gotSnapshot, wantSnapshot) {
		t.Fatalf("main snapshot changed: %v", err)
	}
	if len(restored.tasks) != len(a.tasks) {
		t.Fatal("worker count changed")
	}
	for id, want := range a.tasks {
		got := restored.tasks[id]
		if got == nil || got.id != want.id || got.providerSessionID != want.providerSessionID || !reflect.DeepEqual(got.snapshot, want.snapshot) {
			t.Fatal("worker identity or private snapshot changed")
		}
	}
	if accepted, err := restored.Submit(trigger); accepted || err != nil {
		t.Fatalf("duplicate: accepted=%v err=%v", accepted, err)
	}
	changed := trigger
	changed.Text = "different contents"
	if accepted, err := restored.Submit(changed); accepted || err == nil {
		t.Fatal("conflicting duplicate was not rejected")
	}
	h := restored.newMainHistory()
	defer h.close()
	gotData, gotLinks := historyReadAll(t, h, ref, 4096, 32)
	if gotData != wantData || !reflect.DeepEqual(gotLinks, wantLinks) {
		t.Fatal("stable public reference changed after replay")
	}
	for key := range h.nodes {
		data, _ := historyReadAll(t, h, HistoryRef{Domain: ref.Domain, Kind: key.kind, ID: key.id}, 4096, 32)
		if strings.Contains(data, "PRIVATE_") {
			t.Fatal("public history exposed a private snapshot")
		}
	}
	if callbacks.Load() != wantCalls || len(loaded.Log()) != entries {
		t.Fatal("restore dispatched callbacks or appended records")
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("replay-only operations changed journal bytes: %v", err)
	}
}
