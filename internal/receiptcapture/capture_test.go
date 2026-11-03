package receiptcapture

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestReservationAndClosure(t *testing.T) {
	ctx, h, err := Begin(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if Require(ctx) == nil || Commit(ctx) {
		t.Fatal("ledger is not an invocation slot")
	}
	ctx1, seal1, err := Enter(ctx, "agent", "same", "one")
	if err != nil {
		t.Fatal(err)
	}
	ctx2, seal2, err := Enter(ctx1, "agent", "same", "two")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = Enter(ctx1, "agent", "third", "three"); err == nil {
		t.Fatal("capacity exceeded")
	}
	snap := h.Snapshot()
	if snap[1].ParentToken() != snap[0].Token() || snap[0].Token() == snap[1].Token() {
		t.Fatal(snap)
	}
	if !Commit(ctx2) {
		t.Fatal("commit rejected")
	}
	seal2()
	seal1()
	CloseRegistration(h)
	if Commit(ctx2) || Require(ctx1) == nil {
		t.Fatal("sealed call accepted report")
	}
	if snap[1].Committed() || snap[1].Completed() {
		t.Fatal("snapshot mutated")
	}
	snap[0] = Receipt{}
	if h.Snapshot()[0].Token() == 0 {
		t.Fatal("caller mutated ledger")
	}
	if err = h.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, err = Enter(ctx, "agent", "late", "late"); err == nil {
		t.Fatal("late root admission")
	}
}
func TestReportSealRace(t *testing.T) {
	for range 100 {
		ctx, h, _ := Begin(context.Background(), 1)
		ctx, seal, _ := Enter(ctx, "agent", "1", "tool")
		var wg sync.WaitGroup
		wg.Add(2)
		accepted := false
		go func() { defer wg.Done(); accepted = Commit(ctx) }()
		go func() { defer wg.Done(); seal() }()
		wg.Wait()
		if accepted && !h.Snapshot()[0].Committed() {
			t.Fatal("accepted commit lost")
		}
		CloseRegistration(h)
	}
}
func TestLimitsAndUnsupportedContexts(t *testing.T) {
	for _, limit := range []int{-1, 0, 4097} {
		if _, _, err := Begin(context.Background(), limit); err == nil {
			t.Fatal(limit)
		}
	}
	if _, _, err := Begin(Block(context.Background()), 1); err == nil {
		t.Fatal("blocked begin")
	}
	ctx, h, _ := Begin(context.Background(), 2)
	defer CloseRegistration(h)
	if _, _, err := Begin(ctx, 1); err == nil {
		t.Fatal("nested begin")
	}
	for _, name := range []string{strings.Repeat("x", 4097), string([]byte{255})} {
		if _, _, err := Enter(ctx, "agent", "id", name); err == nil {
			t.Fatal("invalid identity")
		}
	}
	child, seal, _ := Enter(ctx, "agent", "id", "name")
	defer seal()
	if Commit(Block(child)) || Require(Block(child)) == nil {
		t.Fatal("block ignored")
	}
	if _, _, err := Enter(Block(child), "agent", "id", "name"); err == nil {
		t.Fatal("blocked entry")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if h.Wait(canceled) == nil {
		t.Fatal("wait must not finish during registration/active call")
	}
}
func TestClosedRootAllowsOnlyLiveDescendants(t *testing.T) {
	ctx, h, _ := Begin(context.Background(), 3)
	parent, seal, _ := Enter(ctx, "agent", "p", "p")
	CloseRegistration(h)
	child, end, err := Enter(parent, "agent", "c", "c")
	if err != nil {
		t.Fatal(err)
	}
	seal()
	if _, _, err = Enter(parent, "agent", "late", "late"); err == nil {
		t.Fatal("sealed parent admitted child")
	}
	if !Commit(child) {
		t.Fatal("actual active child cannot report")
	}
	end()
	if err = h.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestJSONDoesNotSilentlyDiscardReceipts(t *testing.T) {
	_, h, _ := Begin(context.Background(), 1)
	defer CloseRegistration(h)
	r := Receipt{}
	for _, v := range []any{r, []Receipt{r}, h, struct{ Receipt Receipt }{r}, struct{ Handle *Handle }{h}} {
		if b, err := json.Marshal(v); err == nil {
			t.Fatalf("encoded as %s", b)
		}
	}
	if json.Unmarshal([]byte(`{}`), &r) == nil {
		t.Fatal("receipt restored from empty object")
	}
	if json.Unmarshal([]byte(`{}`), h) == nil {
		t.Fatal("handle restored from empty object")
	}
}
