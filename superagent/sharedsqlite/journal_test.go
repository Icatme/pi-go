//go:build (linux || darwin || windows) && (amd64 || arm64)

package sharedsqlite

import (
	"context"
	"errors"
	"github.com/Icatme/pi-go/agent/session"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func transition(id string) session.NewEntry {
	return session.NewEntry{ID: id, Type: session.EntryTypeCustom, Custom: &session.CustomData{CustomType: "superagent.transition", Payload: []byte(`{"event":"test"}`)}}
}
func TestPersistenceDetachedAndLease(t *testing.T) {
	name := "journal +%?#.sqlite"
	if runtime.GOOS == "windows" {
		name = "journal +%#.sqlite" // A literal question mark is not a valid filename on Windows.
	}
	p := filepath.Join(privateDir(t), name)
	j, err := Open(context.Background(), p, Options{Domain: "team", Owner: "one", LeaseTTL: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), p, Options{Domain: "team", Owner: "two", LeaseTTL: time.Second}); !errors.Is(err, ErrBusy) {
		t.Fatalf("want busy: %v", err)
	}
	e, err := j.AppendEntry(session.MainLane, transition("1"))
	if err != nil {
		t.Fatal(err)
	}
	e.Custom.Payload[0] = 'x'
	log := j.Log()
	log[0].Entry.Custom.Payload[0] = 'y'
	if j.Log()[0].Entry.Custom.Payload[0] != '{' {
		t.Fatal("aliased log")
	}
	epoch := j.Epoch()
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	k, err := Open(context.Background(), p, Options{Domain: "team", Owner: "two", LeaseTTL: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if k.Epoch() <= epoch || len(k.Log()) != 1 {
		t.Fatal("restart lost epoch or entry")
	}
	if _, err := k.AppendEntry(session.MainLane, transition("2")); err != nil {
		t.Fatal(err)
	}
}
func TestExpiredLeaseCannotRenewOrAppend(t *testing.T) {
	p := filepath.Join(privateDir(t), "state.db")
	j, err := Open(context.Background(), p, Options{Domain: "d", Owner: "o", LeaseTTL: 60 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	j.stopRenewal()
	time.Sleep(100 * time.Millisecond)
	if err := j.Renew(); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("renew: %v", err)
	}
	select {
	case <-j.Context().Done():
	default:
		t.Fatal("context not canceled")
	}
	k, err := Open(context.Background(), p, Options{Domain: "d", Owner: "new", LeaseTTL: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if k.Epoch() <= j.Epoch() {
		t.Fatal("epoch not fenced")
	}
	if _, err := j.AppendEntry(session.MainLane, transition("1")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale append: %v", err)
	}
}

func TestRenewalAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	j, err := Open(ctx, filepath.Join(privateDir(t), "state.db"), Options{Domain: "d", Owner: "one", LeaseTTL: 90 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	time.Sleep(200 * time.Millisecond)
	if err := j.Renew(); err != nil {
		t.Fatalf("automatic renewal failed: %v", err)
	}
	cancel()
	if _, err := j.AppendEntry(session.MainLane, transition("1")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("canceled append: %v", err)
	}
}
func TestAppendFailureClosesLease(t *testing.T) {
	j, err := Open(context.Background(), filepath.Join(privateDir(t), "state.db"), Options{Domain: "d", Owner: "o"})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err = j.db.Exec(`CREATE TRIGGER fail_insert BEFORE INSERT ON transitions BEGIN SELECT RAISE(ABORT,'test rollback'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = j.AppendEntry(session.MainLane, transition("1")); err == nil {
		t.Fatal("expected insert failure")
	}
	if len(j.Log()) != 0 {
		t.Fatal("cache changed on failure")
	}
	select {
	case <-j.Context().Done():
	default:
		t.Fatal("failure did not cancel")
	}
	var seq, count int
	if err = j.db.QueryRow("SELECT seq FROM domains WHERE domain='d'").Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if err = j.db.QueryRow("SELECT count(*) FROM transitions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if seq != 0 || count != 0 {
		t.Fatal("transaction was partially committed")
	}
	if _, err = j.AppendEntry(session.MainLane, transition("1")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("not fail closed: %v", err)
	}
}

func TestDifferentDomainsAndStaleClose(t *testing.T) {
	p := filepath.Join(privateDir(t), "state.db")
	j, err := Open(context.Background(), p, Options{Domain: "d", Owner: "one", LeaseTTL: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	other, err := Open(context.Background(), p, Options{Domain: "other", Owner: "two"})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	j.stopRenewal()
	time.Sleep(80 * time.Millisecond)
	k, err := Open(context.Background(), p, Options{Domain: "d", Owner: "replacement"})
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if _, err = j.AppendEntry("main", transition("1")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale append without prior renew: %v", err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	if err = k.Renew(); err != nil {
		t.Fatalf("stale close released replacement: %v", err)
	}
}
func TestRejectSchemaAndUnsafePaths(t *testing.T) {
	ctx := context.Background()
	for _, p := range []string{"", ":memory:", "file:state.db?mode=memory"} {
		if _, err := Open(ctx, p, Options{Domain: "d", Owner: "o"}); err == nil {
			t.Fatalf("accepted %q", p)
		}
	}
	if runtime.GOOS == "windows" {
		for _, p := range []string{`\\localhost\share\state.db`, `\\?\C:\state.db`, `\\.\C:\state.db`} {
			if _, err := connect(ctx, p); err == nil || !strings.Contains(err.Error(), "UNC and device paths") {
				t.Fatalf("expected explicit namespace rejection for %q: %v", p, err)
			}
		}
	}
	for _, change := range []string{"PRAGMA user_version=2", "CREATE TABLE unexpected(x)", "CREATE VIEW unexpected AS SELECT 1"} {
		p := filepath.Join(privateDir(t), "state.db")
		db, err := connect(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(change); err != nil {
			t.Fatal(err)
		}
		db.Close()
		if _, err = Open(ctx, p, Options{Domain: "d", Owner: "o"}); err == nil {
			t.Fatalf("accepted schema change: %s", change)
		}
	}
}
func TestPrivateDatabaseAndJournal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows access is controlled by caller-owned directory ACL")
	}
	p := filepath.Join(privateDir(t), "private.db")
	db, err := connect(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("database mode %o", info.Mode().Perm())
	}
	tx, err := begin(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO domains VALUES('d',x'00','',0,0,0)"); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(p + "-journal")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0077 != 0 {
		t.Fatalf("journal exposed: %o", info.Mode().Perm())
	}
	tx.Rollback()
	link := filepath.Join(privateDir(t), "symlink.db")
	if err = os.Symlink(p, link); err != nil {
		t.Fatal(err)
	}
	if _, err = connect(context.Background(), link); err == nil {
		t.Fatal("accepted symlink")
	}
	if err = os.Chmod(p, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = connect(context.Background(), p); err == nil {
		t.Fatal("accepted public database")
	}
}
func TestAutomaticRenewFailureCancels(t *testing.T) {
	j, err := Open(context.Background(), filepath.Join(privateDir(t), "state.db"), Options{Domain: "d", Owner: "o", LeaseTTL: 90 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err = j.db.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-j.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("renewal error did not cancel context")
	}
	if _, err = j.AppendEntry("main", transition("1")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("renewal failure not closed: %v", err)
	}
}

func privateDir(t *testing.T) string {
	t.Helper()
	p := t.TempDir()
	if err := os.Chmod(p, 0700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNarrowContractRejectsInvalidEntryWithoutLosingLease(t *testing.T) {
	j, err := Open(context.Background(), filepath.Join(privateDir(t), "state.db"), Options{Domain: "d", Owner: "o"})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if _, err = j.AppendEntry("other", transition("1")); err == nil {
		t.Fatal("accepted non-main lane")
	}
	if _, err = j.AppendEntry("main", transition("2")); err == nil {
		t.Fatal("accepted nonsequential ID")
	}
	bad := transition("1")
	bad.Custom.CustomType = "other"
	if _, err = j.AppendEntry("main", bad); err == nil {
		t.Fatal("accepted other custom type")
	}
	if _, err = j.AppendEntry("main", transition("1")); err != nil {
		t.Fatalf("validation lost lease: %v", err)
	}
}
