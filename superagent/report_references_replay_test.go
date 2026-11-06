package superagent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Icatme/pi-go/agent/session"
)

func TestReportReferencesJournalBoundaryAndForgedReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.jsonl")
	route := func(ctx context.Context, job MainJob) (MainOutcome, error) {
		out, err := mainFixture(ctx, job)
		if job.Input.Kind == "worker_result" {
			for _, ref := range job.Input.HistoryRefs {
				if ref.Kind == "report" {
					out.Decision.ReportRefs = []HistoryRef{ref}
				}
			}
		}
		return out, err
	}
	a, err := OpenMain(t.Context(), path, 1, route, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	submit(t, a, Trigger{ID: "one", Source: "im", Thread: "chat", Kind: "request"})
	waitIdle(t, a)
	want := a.Replies()[0]
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	var prefix []byte
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var entry session.LogItem
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatal(err)
		}
		var tr transition
		if entry.Entry == nil || entry.Entry.Custom == nil {
			prefix = append(prefix, line...)
			continue
		}
		if err := json.Unmarshal(entry.Entry.Custom.Payload, &tr); err != nil {
			t.Fatal(err)
		}
		if tr.State != "main_decided" || len(tr.Main.Decision.ReportRefs) == 0 {
			prefix = append(prefix, line...)
			continue
		}
		for _, after := range []bool{false, true} {
			copyPath := filepath.Join(t.TempDir(), "boundary.jsonl")
			raw := append([]byte{}, prefix...)
			if after {
				raw = append(raw, line...)
			}
			if err := os.WriteFile(copyPath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			b, err := OpenMain(t.Context(), copyPath, 1, route, func(context.Context, Job) (Outcome, error) { t.Error("terminal worker rerun"); return Outcome{}, nil })
			if err != nil {
				t.Fatal(err)
			}
			waitIdle(t, b)
			if got := b.Replies(); len(got) != 1 || !reflect.DeepEqual(want, got[0]) {
				t.Fatalf("boundary changed citation: %+v", got)
			}
			if err := b.Close(); err != nil {
				t.Fatal(err)
			}
		}
		for _, mode := range []string{"foreign", "unreported", "wrong-kind", "duplicate", "non-reply", "empty"} {
			t.Run(mode, func(t *testing.T) {
				var bad transition
				raw, _ := json.Marshal(tr)
				if err := json.Unmarshal(raw, &bad); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "foreign":
					bad.Main.Decision.ReportRefs[0].Domain = "other"
				case "unreported":
					bad.Main.Decision.ReportRefs[0].ID = "delivery-2"
				case "wrong-kind":
					bad.Main.Decision.ReportRefs[0].Kind = "task"
				case "duplicate":
					bad.Main.Decision.ReportRefs = append(bad.Main.Decision.ReportRefs, bad.Main.Decision.ReportRefs[0])
				case "non-reply":
					bad.Main.Decision.Action = "ignore"
					bad.Main.Decision.Text = ""
					bad.Main.ReplyID = ""
				}
				changed := entry
				entryCopy := *entry.Entry
				changed.Entry = &entryCopy
				custom := *entry.Entry.Custom
				changed.Entry.Custom = &custom
				changed.Entry.Custom.Payload, _ = json.Marshal(bad)
				if mode == "empty" {
					var fields map[string]any
					json.Unmarshal(changed.Entry.Custom.Payload, &fields)
					fields["main"].(map[string]any)["decision"].(map[string]any)["report_refs"] = []any{}
					changed.Entry.Custom.Payload, _ = json.Marshal(fields)
				}
				changedLine, _ := json.Marshal(changed)
				target := filepath.Join(t.TempDir(), "forged.jsonl")
				raw = append(append([]byte{}, prefix...), append(changedLine, '\n')...)
				if err := os.WriteFile(target, raw, 0600); err != nil {
					t.Fatal(err)
				}
				b, err := OpenMain(t.Context(), target, 1, route, mainWorker)
				if err == nil {
					b.Close()
					t.Fatal("forged citation replay accepted")
				}
			})
		}
		return
	}
	t.Fatal("missing persisted citation decision")
}

// Preserve the new base's failed-entry action barrier when MainDecision becomes
// non-comparable: even an empty but present reference selection is not a zero
// decision and may not be hidden in a failure record.
func TestReportReferencesFailedInputCannotHideSelection(t *testing.T) {
	for _, batch := range []bool{false, true} {
		a, _ := threadStoppedFixture(t)
		submit(t, a, Trigger{ID: "failed-summary", Source: "im", Thread: "chat", Kind: "request"})
		a.mu.Lock()
		input := a.main.inbox[a.main.cursor].input.ID
		before := len(a.main.replies)
		for _, refs := range [][]HistoryRef{{}, {{Domain: a.main.id, Kind: "report", ID: "delivery-1"}}} {
			child := mainTransition{InputID: input, Decision: MainDecision{ReportRefs: refs}}
			tr := transition{State: "main_failed", Text: "failure", Main: &child}
			if batch {
				tr.State = "main_batch_failed"
				tr.Main = &mainTransition{Batch: []mainTransition{child}}
			}
			if err := a.commit(tr); err == nil {
				t.Fatal("failed input accepted hidden references")
			}
		}
		if len(a.main.replies) != before || a.main.inbox[a.main.cursor].input.ID != input {
			t.Fatal("invalid failure changed state")
		}
		a.mu.Unlock()
	}
}
