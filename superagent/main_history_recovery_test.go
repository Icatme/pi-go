package superagent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/session"
)

func TestMainHistoryRebuildAtEveryJournalBoundary(t *testing.T) {
	route := func(_ context.Context, job MainJob) (MainOutcome, error) {
		action := "ignore"
		if job.Input.Kind == "user" {
			action = "new"
		}
		return MainOutcome{Decision: MainDecision{Action: action}, Snapshot: job.Snapshot}, nil
	}
	work := func(_ context.Context, job Job) (Outcome, error) {
		return Outcome{Summary: "public conclusion", Snapshot: agent.AgentSnapshot{SessionID: job.TaskID, Messages: []agent.Message{agent.NewUserTextMessage("private recovery transcript")}}}, nil
	}
	path := filepath.Join(t.TempDir(), "source.jsonl")
	a, err := OpenMain(t.Context(), path, 1, route, work)
	if err != nil {
		t.Fatal(err)
	}
	trigger := Trigger{ID: "original", Source: "im", Thread: "chat", Kind: "request", Text: "public request"}
	submit(t, a, trigger)
	waitIdle(t, a)
	domain := a.main.id
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var prefix []byte
	for n, line := range bytes.SplitAfter(raw, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		prefix = append(prefix, line...)
		if n == 0 {
			continue
		}
		var entry session.LogItem
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatal(err)
		}
		var transition transition
		if err := json.Unmarshal(entry.Entry.Custom.Payload, &transition); err != nil {
			t.Fatal(err)
		}
		t.Run(transition.State+"-"+entry.Entry.ID, func(t *testing.T) {
			cut := filepath.Join(t.TempDir(), "cut.jsonl")
			if err := os.WriteFile(cut, prefix, 0600); err != nil {
				t.Fatal(err)
			}
			b, err := OpenMain(t.Context(), cut, 1, route, work)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			if _, err := b.Submit(trigger); err != nil {
				t.Fatal(err)
			}
			waitIdle(t, b)
			if accepted, err := b.Submit(trigger); accepted || err != nil {
				t.Fatal("recovery admitted a duplicate")
			}
			b.mu.Lock()
			h := b.newMainHistory()
			b.mu.Unlock()
			defer h.close()
			ref := HistoryRef{Domain: domain, Kind: "trigger", ID: "delivery-1"}
			data, links := historyReadAll(t, h, ref, 4096, 32)
			var got Trigger
			if err := json.Unmarshal([]byte(data), &got); err != nil || got != trigger {
				t.Fatalf("trigger identity changed: %v", err)
			}
			reportRef := historyLink(t, links, "report", "delivery-1")
			data, _ = historyReadAll(t, h, reportRef, 4096, 32)
			var report Report
			if err := json.Unmarshal([]byte(data), &report); err != nil {
				t.Fatal(err)
			}
			if report.TaskID != "task-1" || strings.Contains(data, "private recovery transcript") {
				t.Fatal("replay leaked or lost identity")
			}
			if transition.State == "running" && (report.Status != "interrupted" || !report.NeedsReview) {
				t.Fatal("replay invented success after running crash")
			}
			if len(b.Status().Tasks) != 1 || len(b.ReportsSince(0)) != 1 {
				t.Fatal("history recovery duplicated work")
			}
		})
	}
}
