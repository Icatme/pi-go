//go:build linux

package helper

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent/background"
	"github.com/Icatme/pi-go/agent/session"
)

func reserveOnly(t *testing.T, b *Backend, id string) operation {
	t.Helper()
	r := request(id)
	epoch, e := b.authorize(t.Context(), Authorization{Action: "start", ID: id, Request: &r})
	if e != nil {
		t.Fatal(e)
	}
	o := operation{ID: id, Reserved: true, Manifest: &manifest{Version: 1, Grant: b.grant, PolicyRevision: b.opts.PolicyRevision, Epoch: epoch, Request: r}, Observed: background.Observation{ID: id, State: "unknown"}}
	if e := b.persist(&o, epoch); e != nil {
		t.Fatal(e)
	}
	return o
}

func TestCrashBeforeDispatchRemainsReserved(t *testing.T) {
	for _, stage := range []string{"reservation", "request", "claim"} {
		t.Run(stage, func(t *testing.T) {
			b, cfg := fixture(t)
			o := reserveOnly(t, b, "one")
			dir := b.operationDir(o.ID)
			if stage != "reservation" {
				if e := os.Mkdir(dir, 0700); e != nil {
					t.Fatal(e)
				}
				if e := writeNew(filepath.Join(dir, "request.json"), o.Manifest); e != nil {
					t.Fatal(e)
				}
			}
			if stage == "claim" {
				if e := writeNew(filepath.Join(dir, "claim.json"), digest(o.Manifest)); e != nil {
					t.Fatal(e)
				}
			}
			b.Close()
			next, e := Open(b.dir, cfg)
			if e != nil {
				t.Fatal(e)
			}
			defer next.Close()
			if e := next.Start(t.Context(), request(o.ID)); e == nil {
				t.Fatal("unacknowledged reservation was retried")
			}
			observe(t, next, o.ID, "unknown")
			if e := next.Start(t.Context(), request("second")); e == nil {
				t.Fatal("uncertainty released quota")
			}
			if _, e := os.Stat(filepath.Join(dir, "entered")); !os.IsNotExist(e) {
				t.Fatal("recovery launched a body")
			}
		})
	}
}

func TestDurablyObservedFactsSurviveBadSnapshots(t *testing.T) {
	for _, mode := range []string{"missing", "malformed", "regressed", "identity", "false exit"} {
		t.Run(mode, func(t *testing.T) {
			b, cfg := fixture(t)
			o := reserveOnly(t, b, "one")
			dir := b.operationDir(o.ID)
			if e := os.Mkdir(dir, 0700); e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(dir, "observation.json")
			s := snapshot{Digest: digest(o.Manifest), Observation: background.Observation{ID: o.ID, State: "running", Receipts: []background.Receipt{{Token: 1, Domain: "agent", CallID: "call", ToolName: "work", Committed: true}}}}
			if e := replaceJSON(path, s); e != nil {
				t.Fatal(e)
			}
			first := observe(t, b, o.ID, "unknown")
			if len(first.Receipts) != 1 || !first.Receipts[0].Committed {
				t.Fatal("fixture facts not persisted")
			}
			switch mode {
			case "missing":
				os.Remove(path)
			case "malformed":
				os.WriteFile(path, []byte(`{"digest":`), 0600)
			case "regressed":
				s.Observation.Receipts[0].Committed = false
				replaceJSON(path, s)
			case "identity":
				s.Digest = strings.Repeat("0", 64)
				replaceJSON(path, s)
			case "false exit":
				s.Observation.State = "exited" // body receipt is still incomplete.
				replaceJSON(path, s)
			}
			b.Close()
			next, e := Open(b.dir, cfg)
			if e != nil {
				t.Fatal(e)
			}
			defer next.Close()
			got := observe(t, next, o.ID, "unknown")
			if !reflect.DeepEqual(got, first) {
				t.Fatalf("lost prior durable facts: %+v", got)
			}
			if e := next.Start(t.Context(), request("two")); e == nil {
				t.Fatal("bad snapshot released quota")
			}
		})
	}
}

func TestAuthorityValidationAndDurableEpochFence(t *testing.T) {
	b, cfg := fixture(t)
	epoch := uint64(2)
	deny := false
	b.opts.Authorize = func(context.Context, Authorization) (uint64, error) {
		if deny {
			return 0, errors.New("private reason")
		}
		return epoch, nil
	}
	if _, e := b.Inspect(t.Context(), "one"); e != nil {
		t.Fatal(e)
	}
	deny = true
	for _, action := range []func() error{func() error { return b.Start(t.Context(), request("one")) }, func() error { return b.Stop(t.Context(), "one") }, func() error { _, e := b.Inspect(t.Context(), "one"); return e }} {
		if e := action(); !errors.Is(e, ErrDenied) || strings.Contains(e.Error(), "private") {
			t.Fatalf("authority bypass/leak: %v", e)
		}
	}
	if len(b.ops) != 0 {
		t.Fatal("denial mutated registry")
	}
	b.Close()
	next, e := Open(b.dir, cfg) // cfg callback still claims epoch 1.
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	if _, e := next.Inspect(t.Context(), "one"); !errors.Is(e, ErrDenied) {
		t.Fatalf("persisted epoch fence lost: %v", e)
	}
}

func TestAuthorityChangeBetweenReservationAndDispatch(t *testing.T) {
	b, _ := fixture(t)
	calls := 0
	b.opts.Authorize = func(context.Context, Authorization) (uint64, error) {
		calls++
		return uint64(calls), nil
	}
	if e := b.Start(t.Context(), request("one")); !errors.Is(e, ErrDenied) {
		t.Fatalf("epoch change crossed dispatch: %v", e)
	}
	if len(b.ops) != 1 || b.ops["one"].Accepted {
		t.Fatal("uncertain reservation not retained")
	}
	if _, e := os.Stat(filepath.Join(b.operationDir("one"), "claim.json")); !os.IsNotExist(e) {
		t.Fatal("helper launched after authority change")
	}
}

func TestReopenRejectsChangedGrantAndCorruptRegistry(t *testing.T) {
	for _, mode := range []string{"scope", "policy", "command", "limit", "tail"} {
		t.Run(mode, func(t *testing.T) {
			b, cfg := fixture(t)
			b.Close()
			before, _ := os.ReadFile(filepath.Join(b.dir, "registry.jsonl"))
			switch mode {
			case "scope":
				cfg.Scope = "new"
			case "policy":
				cfg.PolicyRevision = "v2"
			case "command":
				cfg.Helpers[0].Args = append(cfg.Helpers[0].Args, "new-argument")
			case "limit":
				cfg.MaxConcurrent = 2
			case "tail":
				before = append(before, []byte(`{"broken"`)...)
				os.WriteFile(filepath.Join(b.dir, "registry.jsonl"), before, 0600)
			}
			if _, e := Open(b.dir, cfg); e == nil {
				t.Fatal("changed/corrupt registry accepted")
			}
			after, _ := os.ReadFile(filepath.Join(b.dir, "registry.jsonl"))
			if string(before) != string(after) {
				t.Fatal("Open repaired or rewrote registry")
			}
		})
	}
}

func TestRequestBoundaryAndDetachedAuthorization(t *testing.T) {
	b, _ := fixture(t)
	b.opts.Authorize = func(context.Context, Authorization) (uint64, error) {
		t.Error("invalid request reached host authorization")
		return 0, ErrDenied
	}
	for name, mutate := range map[string]func(*background.Request){
		"wrong scope":         func(r *background.Request) { r.Scope = "wrong" },
		"unregistered tool":   func(r *background.Request) { r.Call.Name = "/bin/sh" },
		"non-object":          func(r *background.Request) { r.Call.Arguments = json.RawMessage(`[]`) },
		"invalid Unicode":     func(r *background.Request) { r.Call.Arguments = json.RawMessage(`{"name":"\ud800"}`) },
		"parsed arguments":    func(r *background.Request) { r.Call.ParsedArgs = map[string]any{"command": "x"} },
		"empty parsed object": func(r *background.Request) { r.Call.ParsedArgs = map[string]any{} },
	} {
		r := request("one")
		mutate(&r)
		if e := b.Start(t.Context(), r); !errors.Is(e, ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, e)
		}
	}
	if b.seq != 0 || b.failed {
		t.Fatal("invalid input changed the registry")
	}
	b.opts.Authorize = func(_ context.Context, a Authorization) (uint64, error) {
		if a.Request != nil {
			a.Request.Call.Arguments[0] = '!'
		}
		return 1, nil
	}
	r := request("one")
	if e := b.Start(t.Context(), r); e != nil {
		t.Fatal(e)
	}
	dir := entered(t, b, r.ID)
	marker(t, dir, "release")
	observe(t, b, r.ID, "exited")
	if string(r.Call.Arguments) != "{}" {
		t.Fatal("authorizer mutated caller")
	}
}

func TestStopIntentRedeliveredAfterCrashBeforeNotification(t *testing.T) {
	b, cfg := fixture(t)
	r := request("pending-stop")
	if e := b.Start(t.Context(), r); e != nil {
		t.Fatal(e)
	}
	dir := entered(t, b, r.ID)
	// Reproduce the exact crash boundary: intent appended, marker not written.
	o := b.ops[r.ID]
	o.Stopped = true
	if e := b.persist(&o, b.epoch); e != nil {
		t.Fatal(e)
	}
	b.Close()
	next, e := Open(b.dir, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	got := observe(t, next, r.ID, "exited")
	if !got.Failed {
		t.Fatal("surviving helper did not observe recovered stop")
	}
	if _, e := os.Stat(filepath.Join(dir, "canceled")); e != nil {
		t.Fatal("recovered stop did not reach native body")
	}
}

func TestUnknownStopTombstoneDoesNotConsumeAdmissionQuota(t *testing.T) {
	b, _ := fixture(t)
	if e := b.Stop(t.Context(), "unseen"); e != nil {
		t.Fatal(e)
	}
	if e := b.Start(t.Context(), request("unseen")); e == nil {
		t.Fatal("tombstone dispatched")
	}
	if e := b.Start(t.Context(), request("new")); e != nil {
		t.Fatalf("unadmitted tombstone consumed quota: %v", e)
	}
	dir := entered(t, b, "new")
	marker(t, dir, "release")
	observe(t, b, "new", "exited")
}

func TestCorruptStopMarkerCancelsRatherThanPermitsExecution(t *testing.T) {
	b, _ := fixture(t)
	if e := b.Start(t.Context(), request("corrupt-stop")); e != nil {
		t.Fatal(e)
	}
	dir := entered(t, b, "corrupt-stop")
	if e := os.WriteFile(filepath.Join(dir, "stop.json"), []byte(`{"broken"`), 0600); e != nil {
		t.Fatal(e)
	}
	got := observe(t, b, "corrupt-stop", "exited")
	if !got.Failed || len(got.Receipts) != 1 || got.Receipts[0].Committed {
		t.Fatalf("corrupt stop authorized native continuation: %+v", got)
	}
}

func TestTerminalEvidenceWinsOverFailedStopRedelivery(t *testing.T) {
	b, _ := fixture(t)
	o := reserveOnly(t, b, "terminal")
	o.Stopped = true
	if e := b.persist(&o, b.epoch); e != nil {
		t.Fatal(e)
	}
	dir := b.operationDir(o.ID)
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "stop.json"), []byte(`invalid`), 0600); e != nil {
		t.Fatal(e)
	}
	s := snapshot{Digest: digest(o.Manifest), Observation: background.Observation{ID: o.ID, State: "exited", Failed: true}}
	if e := replaceJSON(filepath.Join(dir, "observation.json"), s); e != nil {
		t.Fatal(e)
	}
	b.opts.Authorize = func(_ context.Context, a Authorization) (uint64, error) {
		if a.Action == "stop" {
			return 0, ErrDenied
		}
		return 1, nil
	}
	observe(t, b, o.ID, "exited")
}

func TestMaximumEscapedReceiptsCanBeReadAndRetained(t *testing.T) {
	b, _ := fixture(t)
	o := reserveOnly(t, b, "large")
	dir := b.operationDir(o.ID)
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	s := snapshot{Digest: digest(o.Manifest), Observation: background.Observation{ID: o.ID, State: "exited", Output: json.RawMessage(`"` + strings.Repeat("x", (64<<10)-2) + `"`)}}
	for i := range 256 {
		s.Observation.Receipts = append(s.Observation.Receipts, background.Receipt{Token: uint64(i + 1), Domain: "agent", CallID: strings.Repeat("\x00", 4096), ToolName: strings.Repeat("\x01", 4096), Committed: true, Completed: true})
	}
	if e := replaceJSON(filepath.Join(dir, "observation.json"), s); e != nil {
		t.Fatal(e)
	}
	got := observe(t, b, o.ID, "exited")
	if len(got.Receipts) != 256 || len(got.Output) != 64<<10 {
		t.Fatal("maximum valid observation stranded")
	}
}

func TestReplayRejectsImpossibleAdmissionTransitions(t *testing.T) {
	for _, mode := range []string{"tombstone accepted", "tombstone exited", "stale admission", "excess reservation"} {
		t.Run(mode, func(t *testing.T) {
			b, cfg := fixture(t)
			var bad operation
			switch mode {
			case "tombstone accepted", "tombstone exited":
				if e := b.Stop(t.Context(), "one"); e != nil {
					t.Fatal(e)
				}
				_ = b.Start(t.Context(), request("one"))
				bad = clone(b.ops["one"])
				if mode == "tombstone accepted" {
					bad.Accepted = true
				} else {
					bad.Observed.State = "exited"
				}
			case "stale admission":
				b.opts.Authorize = func(context.Context, Authorization) (uint64, error) { return 2, nil }
				if _, e := b.Inspect(t.Context(), "one"); e != nil {
					t.Fatal(e)
				}
				bad = operation{ID: "one", Reserved: true, Manifest: &manifest{Version: 1, Grant: b.grant, PolicyRevision: cfg.PolicyRevision, Epoch: 1, Request: request("one")}, Observed: background.Observation{ID: "one", State: "unknown"}}
			case "excess reservation":
				bad = clone(reserveOnly(t, b, "one"))
				bad.ID, bad.Manifest.Request.ID, bad.Observed.ID = "two", "two", "two"
			}
			// Bypass the runtime validator to model an impossible durable record.
			raw, _ := json.Marshal(record{Epoch: b.epoch, Operation: &bad})
			if _, e := b.store.AppendEntry(session.MainLane, session.NewEntry{ID: strconv.FormatUint(b.seq+1, 10), Type: session.EntryTypeCustom, Custom: &session.CustomData{CustomType: "helper.v1", Payload: raw}}); e != nil {
				t.Fatal(e)
			}
			b.Close()
			before, _ := os.ReadFile(filepath.Join(b.dir, "registry.jsonl"))
			if _, e := Open(b.dir, cfg); e == nil {
				t.Fatalf("accepted impossible transition: %s", mode)
			}
			after, _ := os.ReadFile(filepath.Join(b.dir, "registry.jsonl"))
			if string(after) != string(before) {
				t.Fatal("failed replay repaired source")
			}
		})
	}
}
