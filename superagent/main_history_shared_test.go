//go:build (linux || darwin || windows) && (amd64 || arm64)

package superagent

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestMainHistorySharedLeaseTakeoverInvalidatesReaderAndCursor(t *testing.T) {
	path := filepath.Join(privateSharedDir(t), "history.db")
	old := sharedJournal(t, path, "old")
	entered := make(chan MainJob, 2)
	release := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	defer finish()
	route := func(_ context.Context, job MainJob) (MainOutcome, error) {
		entered <- job
		<-release // Deliberately keep both callbacks alive across takeover.
		return MainOutcome{Decision: MainDecision{Action: "ignore"}, Snapshot: job.Snapshot}, nil
	}
	a, err := OpenMainJournal(t.Context(), old, MainJournalOptions{Parallel: 1, Router: route, Worker: mainWorker})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { finish(); _ = a.Close() }()
	submit(t, a, Trigger{ID: "long", Source: "im", Thread: "chat", Kind: "request", Text: strings.Repeat("汉<", 500)})
	first := receive(t, entered)
	ref := first.Input.HistoryRefs[0]
	req := HistoryRequest{Ref: ref, MaxBytes: 512, MaxItems: 1}
	p, err := first.History.Read(t.Context(), req)
	if err != nil || p.Next == "" {
		t.Fatalf("first page: %+v %v", p, err)
	}
	// Stop only the asynchronous propagation watcher to deterministically model
	// its scheduling gap. The real backend ownership context must still gate reads.
	if !a.stopJournalWatch() {
		t.Fatal("journal watcher already ran")
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if a.ctx.Err() != nil {
		t.Fatal("fixture lost the propagation gap")
	}
	req.Cursor = p.Next
	for _, request := range []HistoryRequest{req, {Ref: ref, MaxBytes: 512, MaxItems: 1}} {
		if page, err := first.History.Read(context.Background(), request); err == nil || !reflect.DeepEqual(page, HistoryPage{}) {
			t.Fatalf("old lease remained readable: %+v %v", page, err)
		}
	}
	replacement := sharedJournal(t, path, "new")
	b, err := OpenMainJournal(t.Context(), replacement, MainJournalOptions{Parallel: 1, Router: route, Worker: mainWorker})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { finish(); _ = b.Close() }()
	next := receive(t, entered)
	if next.SessionID != first.SessionID || next.Input.HistoryRefs[0] != ref {
		t.Fatal("takeover changed durable identity")
	}
	before := replacement.Log()
	if page, err := next.History.Read(t.Context(), req); err == nil || !reflect.DeepEqual(page, HistoryPage{}) {
		t.Fatalf("old owner token survived takeover: %+v %v", page, err)
	}
	req.Cursor = ""
	page, err := next.History.Read(t.Context(), req)
	if err != nil || page.Data != p.Data {
		t.Fatalf("stable reference failed after takeover: %+v %v", page, err)
	}
	if !reflect.DeepEqual(before, replacement.Log()) {
		t.Fatal("history read wrote journal")
	}
	a.cancel()
	finish()
	waitIdle(t, b)
}
