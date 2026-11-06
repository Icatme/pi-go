//go:build linux

package helper

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/background"
	"github.com/Icatme/pi-go/executionreceipt"
)

// Only the package's own test binary is launched. Fixture writes and commit
// receipts have no external effects, credentials, network or user-device access.
func TestNativeHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	dir := os.Args[len(os.Args)-1]
	exists := func(name string) bool { _, e := os.Stat(filepath.Join(dir, name)); return e == nil }
	mark := func(name string) { _ = os.WriteFile(filepath.Join(dir, name), []byte("1"), 0600) }
	cfg := NativeConfig{Scope: "fixture", PolicyRevision: "v1", PollInterval: time.Millisecond,
		Authorize: func(_ context.Context, _ background.Request, epoch uint64) error {
			var current uint64
			if readJSON(filepath.Join(filepath.Dir(dir), "epoch.json"), &current) != nil || current != epoch {
				return ErrDenied
			}
			return nil
		},
		Options: agent.RunToolCallOptions{Tools: []agent.ToolDefinition{{Name: "work", Execute: func(ctx context.Context, tc agent.ToolExecutionContext) (agent.ToolResult, error) {
			// An exclusive marker makes any duplicate physical entry visible.
			if writeNew(filepath.Join(dir, "entered"), os.Getpid()) != nil {
				mark("duplicate")
				return agent.ToolResult{}, fmt.Errorf("duplicate entry")
			}
			if os.Getenv("PIGO_HELPER_MUST_NOT_INHERIT") != "" {
				mark("inherited")
			}
			mode, _ := tc.Args.(map[string]any)["mode"].(string)
			if mode == "crash" {
				executionreceipt.ReportLocalCommit(ctx)
				os.Exit(17)
			}
			if mode == "late" {
				<-ctx.Done()
				mark("canceled")
				executionreceipt.ReportLocalCommit(ctx)
				mark("committed")
				for !exists("release") {
					time.Sleep(time.Millisecond)
				}
				return agent.ToolResult{Details: "late result"}, nil
			}
			for !exists("release") {
				select {
				case <-ctx.Done():
					mark("canceled")
					return agent.ToolResult{}, ctx.Err()
				case <-time.After(time.Millisecond):
				}
			}
			if e := tc.CheckPermission(ctx); e != nil {
				mark("denied")
				return agent.ToolResult{}, e
			}
			executionreceipt.ReportLocalCommit(ctx)
			return agent.ToolResult{Details: "fixture result"}, nil
		}}}},
	}
	if err := Run(context.Background(), dir, cfg); err != nil {
		_ = os.WriteFile(filepath.Join(dir, "run-error"), []byte(err.Error()), 0600)
		os.Exit(2)
	}
	os.Exit(0)
}

func fixture(t *testing.T) (*Backend, Options) {
	t.Helper()
	cfg := config(t)
	b, e := Create(filepath.Join(t.TempDir(), "backend"), cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e := writeNew(filepath.Join(b.dir, "epoch.json"), uint64(1)); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { b.Close() })
	return b, cfg
}
func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("fixture condition did not become true")
}
func marker(t *testing.T, dir, name string) {
	t.Helper()
	if e := os.WriteFile(filepath.Join(dir, name), []byte("1"), 0600); e != nil {
		t.Fatal(e)
	}
}
func entered(t *testing.T, b *Backend, id string) string {
	t.Helper()
	dir := b.operationDir(id)
	waitFor(t, func() bool { _, e := os.Stat(filepath.Join(dir, "entered")); return e == nil })
	return dir
}
func observe(t *testing.T, b *Backend, id, state string) background.Observation {
	t.Helper()
	var o background.Observation
	waitFor(t, func() bool {
		var e error
		o, e = b.Inspect(t.Context(), id)
		if e != nil {
			t.Fatal(e)
		}
		return o.State == state
	})
	return o
}

func TestCoordinatorReopenObservesSameHelperWithoutDispatch(t *testing.T) {
	t.Setenv("PIGO_HELPER_MUST_NOT_INHERIT", "private parent value")
	b, cfg := fixture(t)
	r := request("same")
	if e := b.Start(t.Context(), r); e != nil {
		t.Fatal(e)
	}
	dir := entered(t, b, r.ID)
	t.Cleanup(func() { marker(t, dir, "release") })
	if _, e := Open(b.dir, cfg); e == nil {
		t.Fatal("concurrent writer opened")
	}
	if e := b.Close(); e != nil {
		t.Fatal(e)
	}
	next, e := Open(b.dir, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	if e := next.Start(t.Context(), r); e != nil {
		t.Fatal(e)
	}
	observe(t, next, r.ID, "unknown") // stale running snapshots are not liveness.
	if e := next.Start(t.Context(), request("second")); e == nil {
		t.Fatal("restart released quota")
	}
	marker(t, dir, "release")
	o := observe(t, next, r.ID, "exited")
	if len(o.Receipts) != 1 || !o.Receipts[0].Committed || !o.Receipts[0].Completed || !strings.Contains(string(o.Output), "fixture result") {
		t.Fatalf("lost terminal facts: %+v", o)
	}
	for _, file := range []string{"duplicate", "inherited"} {
		if _, e := os.Stat(filepath.Join(dir, file)); !os.IsNotExist(e) {
			t.Fatalf("unexpected marker %s", file)
		}
	}
	// Once returned, terminal facts are in the parent registry, not dependent on
	// the helper snapshot surviving a later administrative mistake.
	if e := os.Remove(filepath.Join(dir, "observation.json")); e != nil {
		t.Fatal(e)
	}
	next.Close()
	last, e := Open(b.dir, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer last.Close()
	got := observe(t, last, r.ID, "exited")
	if string(got.Output) != string(o.Output) || !got.Receipts[0].Committed {
		t.Fatal("terminal registry lost facts")
	}
}

func TestStopLateCommitAndDuplicateClaim(t *testing.T) {
	b, cfg := fixture(t)
	r := request("late")
	r.Call.Arguments = json.RawMessage(`{"mode":"late"}`)
	if e := b.Start(t.Context(), r); e != nil {
		t.Fatal(e)
	}
	dir := entered(t, b, r.ID)
	t.Cleanup(func() { marker(t, dir, "release") })
	// A second physical helper cannot enter after the durable claim, even if
	// invoked directly by a mistaken host rather than through Backend.Start.
	c := cfg.Helpers[0]
	cmd := exec.Command(c.Executable, append(c.Args, dir)...)
	cmd.Env = []string{}
	if e := cmd.Run(); e == nil {
		t.Fatal("duplicate claim accepted")
	}
	if e := b.Stop(t.Context(), r.ID); e != nil {
		t.Fatal(e)
	}
	waitFor(t, func() bool {
		_, e := os.Stat(filepath.Join(dir, "committed"))
		return e == nil
	})
	observe(t, b, r.ID, "running")
	if e := b.Start(t.Context(), request("second")); e == nil {
		t.Fatal("cancellation released capacity")
	}
	b.Close()
	next, e := Open(b.dir, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	o := observe(t, next, r.ID, "unknown")
	// Local exposes the synchronous body's receipt only after its return. The
	// absent receipt here is unknown, never a claim that the fixture did nothing.
	marker(t, dir, "release")
	o = observe(t, next, r.ID, "exited")
	if len(o.Receipts) != 1 || !o.Receipts[0].Committed || !o.Receipts[0].Completed {
		t.Fatalf("stop erased late execution: %+v", o)
	}
	if _, e := os.Stat(filepath.Join(dir, "duplicate")); !os.IsNotExist(e) {
		t.Fatal("second helper entered")
	}
}

func TestHelperDeathNeverMeansNoEffectOrSafeRetry(t *testing.T) {
	b, cfg := fixture(t)
	r := request("crash")
	r.Call.Arguments = json.RawMessage(`{"mode":"crash"}`)
	// The crash may race the bounded admission receipt. Either outcome is
	// uncertain; neither permits replacing the original operation identity.
	_ = b.Start(t.Context(), r)
	entered(t, b, r.ID)
	observe(t, b, r.ID, "unknown")
	b.Close()
	next, e := Open(b.dir, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer next.Close()
	if e := next.Start(t.Context(), r); e != nil {
		t.Fatal(e)
	}
	if e := next.Start(t.Context(), request("other")); e == nil {
		t.Fatal("helper death released quota")
	}
	observe(t, next, r.ID, "unknown")
	// Even intentionally attempting to restart the dead execution process
	// cannot re-enter the operation body after its durable claim.
	c := cfg.Helpers[0]
	cmd := exec.Command(c.Executable, append(c.Args, next.operationDir(r.ID))...)
	cmd.Env = []string{}
	if e := cmd.Run(); e == nil {
		t.Fatal("dead helper resumed")
	}
}

func TestCurrentAuthorityRevokesSurvivingHelper(t *testing.T) {
	b, _ := fixture(t)
	r := request("revoke")
	if e := b.Start(t.Context(), r); e != nil {
		t.Fatal(e)
	}
	dir := entered(t, b, r.ID)
	if e := replaceJSON(filepath.Join(b.dir, "epoch.json"), uint64(2)); e != nil {
		t.Fatal(e)
	}
	o := observe(t, b, r.ID, "exited")
	if !o.Failed || len(o.Receipts) != 1 || o.Receipts[0].Committed {
		t.Fatalf("old execution gained new epoch: %+v", o)
	}
	marker(t, dir, "release")
}

// This separate process dies while the helper remains behind its barrier.
// Setsid and the lack of parent-owned pipes let the operation outlive it.
func TestCoordinatorProcessFixture(t *testing.T) {
	dir := os.Getenv("PIGO_COORDINATOR_FIXTURE")
	if dir == "" {
		return
	}
	b, e := Create(dir, config(t))
	if e != nil {
		os.Exit(2)
	}
	if writeNew(filepath.Join(dir, "epoch.json"), uint64(1)) != nil || b.Start(context.Background(), request("orphan")) != nil {
		os.Exit(2)
	}
	entered(t, b, "orphan")
	os.Exit(19) // no Close, no deferred cleanup.
}
func TestCoordinatorProcessDeathDoesNotRestartOriginalOperation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backend")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCoordinatorProcessFixture$")
	cmd.Env = []string{"PIGO_COORDINATOR_FIXTURE=" + dir}
	if e := cmd.Run(); e == nil {
		t.Fatal("fixture did not crash")
	}
	b, e := Open(dir, config(t))
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	opdir := b.operationDir("orphan")
	t.Cleanup(func() { marker(t, opdir, "release") })
	if e := b.Start(t.Context(), request("orphan")); e != nil {
		t.Fatal(e)
	}
	observe(t, b, "orphan", "unknown")
	marker(t, opdir, "release")
	observe(t, b, "orphan", "exited")
	var pid int
	if readJSON(filepath.Join(opdir, "entered"), &pid) != nil || pid == 0 {
		t.Fatal("original body never entered")
	}
	// PID is diagnostic only, never completion evidence.
}
