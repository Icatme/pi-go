package sharedpostgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent/session"
)

// This is a scripted database/sql driver, NOT a PostgreSQL emulator. It tests
// ordering, cancellation and uncertainty in the client protocol only. Actual
// PostgreSQL locking/isolation must pass the separately gated integration tests.
type step struct {
	op, sql  string
	rows     [][]driver.Value
	affected *int64
	err      error
	hook     func(context.Context, []driver.NamedValue) error
}
type script struct {
	mu    sync.Mutex
	steps []step
	t     *testing.T
}

func (s *script) next(ctx context.Context, op, query string, args []driver.NamedValue) (step, error) {
	s.mu.Lock()
	if len(s.steps) == 0 {
		s.mu.Unlock()
		return step{}, fmt.Errorf("unexpected %s: %s", op, query)
	}
	x := s.steps[0]
	s.steps = s.steps[1:]
	s.mu.Unlock()
	if x.op != op || (x.sql != "" && !strings.Contains(query, x.sql)) {
		s.t.Errorf("got %s %q, want %s containing %q", op, query, x.op, x.sql)
		return x, errors.New("script mismatch")
	}
	if op != "commit" && op != "rollback" && ctx != nil {
		if _, ok := ctx.Deadline(); !ok {
			s.t.Error("database operation has no deadline")
		}
	}
	if x.hook != nil {
		if err := x.hook(ctx, args); err != nil {
			return x, err
		}
	}
	return x, x.err
}

type connector struct{ s *script }

func (c connector) Connect(context.Context) (driver.Conn, error) { return &connection{c.s}, nil }
func (c connector) Driver() driver.Driver                        { return testDriver{} }

type testDriver struct{}

func (testDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type connection struct{ s *script }

func (*connection) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unexpected Prepare") }
func (*connection) Close() error                        { return nil }
func (c *connection) Begin() (driver.Tx, error)         { return nil, errors.New("unexpected unbounded Begin") }
func (c *connection) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	if o.Isolation != driver.IsolationLevel(sql.LevelReadCommitted) {
		return nil, errors.New("require read committed")
	}
	_, e := c.s.next(ctx, "begin", "", nil)
	return &transaction{c.s}, e
}
func (c *connection) ExecContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Result, error) {
	x, e := c.s.next(ctx, "exec", q, a)
	if x.affected != nil {
		return driver.RowsAffected(*x.affected), e
	}
	return driver.RowsAffected(1), e
}
func (c *connection) QueryContext(ctx context.Context, q string, a []driver.NamedValue) (driver.Rows, error) {
	x, e := c.s.next(ctx, "query", q, a)
	return &resultRows{values: x.rows}, e
}

type transaction struct{ s *script }

func (x *transaction) Commit() error   { _, e := x.s.next(nil, "commit", "", nil); return e }
func (x *transaction) Rollback() error { _, e := x.s.next(nil, "rollback", "", nil); return e }

type resultRows struct{ values [][]driver.Value }

func (r *resultRows) Columns() []string {
	if len(r.values) == 0 {
		return []string{"seq", "record"}
	}
	return make([]string, len(r.values[0]))
}
func (*resultRows) Close() error { return nil }
func (r *resultRows) Next(v []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(v, r.values[0])
	r.values = r.values[1:]
	return nil
}
func scriptedDB(t *testing.T, steps ...step) (*sql.DB, *script) {
	t.Helper()
	s := &script{t: t, steps: steps}
	db := sql.OpenDB(connector{s})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		db.Close()
		// database/sql may finish its automatic rollback in a goroutine after
		// the canceled query returns. Do not mistake that scheduling for a leak.
		until := time.Now().Add(time.Second)
		for time.Now().Before(until) {
			s.mu.Lock()
			n := len(s.steps)
			s.mu.Unlock()
			if n == 0 {
				break
			}
			time.Sleep(time.Millisecond)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.steps) != 0 {
			t.Errorf("%d unused database steps: %+v", len(s.steps), s.steps)
		}
	})
	return db, s
}
func beginSteps() []step {
	return []step{{op: "begin"}, {op: "exec", sql: "set_config('statement_timeout'"}}
}
func clockStep(now time.Time) step {
	return step{op: "query", sql: "SELECT clock_timestamp()", rows: [][]driver.Value{{now}}}
}
func rowStep(owner string, epoch, seq int64, expires time.Time) step {
	b, _ := json.Marshal(session.Header{Kind: session.HeaderKindSession, Version: session.CurrentFormatVersion, ID: "superagent", CreatedAt: 1})
	return step{op: "query", sql: "FOR UPDATE", rows: [][]driver.Value{{b, owner, epoch, expires, seq}}}
}
func claimSteps(ttl time.Duration) []step {
	now := time.Now()
	s := beginSteps()
	return append(s, step{op: "exec", sql: "ON CONFLICT (domain) DO NOTHING"}, rowStep("", 0, 0, now.Add(-time.Second)), clockStep(now), step{op: "exec", sql: "epoch="}, step{op: "query", sql: "ORDER BY seq"}, clockStep(now), step{op: "commit"})
}
func testJournal(t *testing.T, steps []step, ttl time.Duration) (*Journal, *script) {
	t.Helper()
	db, s := scriptedDB(t, steps...)
	j, e := Open(context.Background(), db, Options{Domain: "d", Owner: "o", LeaseTTL: ttl, TransactionTimeout: ttl / 4})
	if e != nil {
		t.Fatal(e)
	}
	// Disable periodic renewal, but deliberately leave the independent deadline
	// watchdog active for every test (no I/O can hold its mutex).
	j.stopRenewal()
	t.Cleanup(j.cancel)
	return j, s
}
func input(id string) session.NewEntry {
	return session.NewEntry{ID: id, Type: session.EntryTypeCustom, Custom: &session.CustomData{CustomType: "superagent.transition", Payload: []byte(`{"event":"test"}`)}}
}

func TestClaimChecksClockAfterLockAndReplay(t *testing.T) {
	now := time.Now()
	s := beginSteps()
	s = append(s, step{op: "exec", sql: "ON CONFLICT"}, rowStep("", 0, 0, now.Add(-time.Second)), clockStep(now), step{op: "exec", sql: "epoch="}, step{op: "query", sql: "ORDER BY seq"}, clockStep(now.Add(time.Hour)), step{op: "rollback"})
	db, _ := scriptedDB(t, s...)
	if _, e := Open(context.Background(), db, Options{Domain: "d", Owner: "o"}); !errors.Is(e, ErrLeaseLost) {
		t.Fatalf("expired replay accepted: %v", e)
	}
}
func TestLockWaitExpiryCannotRenew(t *testing.T) {
	now := time.Now()
	s := claimSteps(time.Second)
	s = append(s, beginSteps()...)
	s = append(s, rowStep("o", 1, 0, now.Add(-time.Millisecond)), clockStep(now), step{op: "rollback"})
	j, _ := testJournal(t, s, time.Second)
	if e := j.Renew(); !errors.Is(e, ErrLeaseLost) {
		t.Fatalf("expired owner renewed: %v", e)
	}
	if j.Context().Err() == nil {
		t.Fatal("ownership stayed live")
	}
}
func TestEpochAndSequenceFenceBeforeAppend(t *testing.T) {
	for _, tc := range []struct {
		name, owner string
		epoch, seq  int64
	}{{"epoch", "o", 2, 0}, {"owner", "new", 1, 0}, {"sequence", "o", 1, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			s := claimSteps(time.Second)
			s = append(s, beginSteps()...)
			s = append(s, rowStep(tc.owner, tc.epoch, tc.seq, now.Add(time.Second)), clockStep(now), step{op: "rollback"})
			j, _ := testJournal(t, s, time.Second)
			if _, e := j.AppendEntry("main", input("1")); !errors.Is(e, ErrLeaseLost) {
				t.Fatalf("fence accepted: %v", e)
			}
			if len(j.Log()) != 0 {
				t.Fatal("cache changed")
			}
		})
	}
}
func appendSteps(now time.Time) []step {
	s := beginSteps()
	return append(s, rowStep("o", 1, 0, now.Add(time.Second)), clockStep(now), step{op: "exec", sql: "SET seq="}, step{op: "exec", sql: "INSERT INTO pi_superagent_v1.transitions"})
}
func TestAppendRechecksExpiryBeforeCommit(t *testing.T) {
	now := time.Now()
	s := claimSteps(time.Second)
	s = append(s, appendSteps(now)...)
	s = append(s, clockStep(now.Add(2*time.Second)), step{op: "rollback"})
	j, _ := testJournal(t, s, time.Second)
	if _, e := j.AppendEntry("main", input("1")); !errors.Is(e, ErrLeaseLost) {
		t.Fatalf("expired commit: %v", e)
	}
	if len(j.Log()) != 0 || j.Context().Err() == nil {
		t.Fatal("not fail closed")
	}
}
func TestUnknownCommitFailsClosedWithoutRetry(t *testing.T) {
	now := time.Now()
	s := claimSteps(time.Second)
	s = append(s, appendSteps(now)...)
	s = append(s, clockStep(now), step{op: "commit", err: errors.New("lost COMMIT acknowledgement")})
	j, _ := testJournal(t, s, time.Second)
	if _, e := j.AppendEntry("main", input("1")); !errors.Is(e, ErrCommitUnknown) {
		t.Fatalf("unknown commit not classified: %v", e)
	}
	if len(j.Log()) != 0 || j.Context().Err() == nil {
		t.Fatal("unknown commit remained usable")
	}
	if _, e := j.AppendEntry("main", input("1")); !errors.Is(e, ErrLeaseLost) {
		t.Fatalf("retry accepted: %v", e)
	}
}
func TestWatchdogDoesNotWaitForIO(t *testing.T) {
	ttl := 300 * time.Millisecond
	entered := make(chan struct{})
	release := make(chan struct{})
	now := time.Now()
	s := claimSteps(ttl)
	s = append(s, beginSteps()...)
	x := rowStep("o", 1, 0, now.Add(ttl))
	x.hook = func(ctx context.Context, _ []driver.NamedValue) error { close(entered); <-release; return ctx.Err() }
	s = append(s, x, step{op: "rollback"})
	j, _ := testJournal(t, s, ttl)
	done := make(chan error, 1)
	go func() { _, e := j.AppendEntry("main", input("1")); done <- e }()
	<-entered
	select {
	case <-j.Context().Done():
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("I/O blocked lease expiry")
	}
	// The misbehaving test driver ignores context. Cancellation still occurs.
	if len(j.Log()) != 0 {
		t.Fatal("snapshot read blocked or changed")
	}
	close(release)
	if e := <-done; e == nil {
		t.Fatal("stalled I/O succeeded")
	}
}
func TestLateRenewalAcknowledgementCannotRevive(t *testing.T) {
	ttl := 300 * time.Millisecond
	now := time.Now()
	s := claimSteps(ttl)
	s = append(s, beginSteps()...)
	s = append(s, rowStep("o", 1, 0, now.Add(ttl)), clockStep(now), step{op: "exec", sql: "SET expires="}, clockStep(now), step{op: "commit", hook: func(context.Context, []driver.NamedValue) error { time.Sleep(2 * ttl); return nil }})
	j, _ := testJournal(t, s, ttl)
	if e := j.Renew(); !errors.Is(e, ErrLeaseLost) && !errors.Is(e, ErrCommitUnknown) {
		t.Fatalf("late acknowledgement revived ownership: %v", e)
	}
	if j.Context().Err() == nil {
		t.Fatal("context revived")
	}
}
func TestAppendDetachedCacheAndCallerOwnedDB(t *testing.T) {
	now := time.Now()
	s := claimSteps(time.Second)
	s = append(s, appendSteps(now)...)
	s = append(s, clockStep(now), step{op: "commit"})
	s = append(s, beginSteps()...)
	s = append(s, rowStep("o", 1, 1, now.Add(time.Second)), step{op: "exec", sql: "owner='',expires="}, step{op: "commit"})
	j, sc := testJournal(t, s, time.Second)
	e, err := j.AppendEntry("main", input("1"))
	if err != nil {
		t.Fatal(err)
	}
	e.Custom.Payload[0] = 'x'
	l := j.Log()
	l[0].Entry.Custom.Payload[0] = 'y'
	if j.Log()[0].Entry.Custom.Payload[0] != '{' {
		t.Fatal("aliased snapshot")
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	// database/sql Ping has no driver operation and fails if Close closed db.
	if err = j.db.PingContext(t.Context()); err != nil {
		t.Fatalf("caller database closed: %v", err)
	}
	_ = sc
}

func TestRenewRechecksOriginalExpiryBeforeCommit(t *testing.T) {
	now := time.Now()
	s := claimSteps(time.Second)
	s = append(s, beginSteps()...)
	s = append(s, rowStep("o", 1, 0, now.Add(50*time.Millisecond)), clockStep(now), step{op: "exec", sql: "SET expires="}, clockStep(now.Add(100*time.Millisecond)), step{op: "rollback"})
	j, _ := testJournal(t, s, time.Second)
	if e := j.Renew(); !errors.Is(e, ErrLeaseLost) {
		t.Fatalf("renewed after original server deadline: %v", e)
	}
}
func TestRenewalReceiptDoesNotAddFullTTL(t *testing.T) {
	ttl := 600 * time.Millisecond
	now := time.Now()
	s := claimSteps(ttl)
	s = append(s, beginSteps()...)
	s = append(s, rowStep("o", 1, 0, now.Add(ttl)), clockStep(now), step{op: "exec", sql: "SET expires="}, clockStep(now), step{op: "commit", hook: func(context.Context, []driver.NamedValue) error { time.Sleep(60 * time.Millisecond); return nil }})
	j, _ := testJournal(t, s, ttl)
	before := time.Now()
	if err := j.Renew(); err != nil {
		t.Fatal(err)
	}
	after := time.Now()
	j.mu.Lock()
	deadline := j.deadline
	j.mu.Unlock()
	if deadline.After(before.Add(ttl+10*time.Millisecond)) || deadline.After(after.Add(ttl-30*time.Millisecond)) {
		t.Fatalf("TTL was measured from acknowledgement: %v", deadline.Sub(after))
	}
}
func TestClaimBusyOverflowAndUnknownCommit(t *testing.T) {
	for _, name := range []string{"busy", "epoch overflow", "unknown commit"} {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			s := claimSteps(time.Second)
			want := ErrBusy
			switch name {
			case "busy":
				s = append(beginSteps(), step{op: "exec", sql: "ON CONFLICT"}, rowStep("other", 9, 0, now.Add(time.Second)), clockStep(now), step{op: "rollback"})
			case "epoch overflow":
				s = append(beginSteps(), step{op: "exec", sql: "ON CONFLICT"}, rowStep("", int64(^uint64(0)>>1), 0, now.Add(-time.Second)), clockStep(now), step{op: "rollback"})
				want = nil
			case "unknown commit":
				s[len(s)-1].err = errors.New("lost ack")
				want = ErrCommitUnknown
			}
			db, _ := scriptedDB(t, s...)
			j, err := Open(t.Context(), db, Options{Domain: "d", Owner: "o"})
			if err == nil || j != nil || (want != nil && !errors.Is(err, want)) {
				t.Fatalf("claim %s: %v", name, err)
			}
		})
	}
}
func TestStaleCloseCannotReleaseReplacement(t *testing.T) {
	now := time.Now()
	s := claimSteps(time.Second)
	s = append(s, beginSteps()...)
	s = append(s, rowStep("o", 2, 1, now.Add(time.Second)), step{op: "rollback"})
	j, _ := testJournal(t, s, time.Second)
	if e := j.Close(); e != nil {
		t.Fatal(e)
	}
}
func TestInvalidAppendIsNonMutating(t *testing.T) {
	j, _ := testJournal(t, claimSteps(time.Second), time.Second)
	bad := input("wrong-id")
	if _, err := j.AppendEntry("main", bad); err == nil {
		t.Fatal("invalid entry accepted")
	}
	if j.Context().Err() != nil || len(j.Log()) != 0 {
		t.Fatal("invalid local input changed ownership")
	}
}
func TestOptionsRejectedBeforeDatabaseAccess(t *testing.T) {
	db, _ := scriptedDB(t)
	for _, o := range []Options{{}, {Domain: "d", Owner: "o", LeaseTTL: time.Millisecond}, {Domain: "d", Owner: "o", LeaseTTL: time.Second, TransactionTimeout: time.Second}} {
		if _, err := Open(t.Context(), db, o); err == nil {
			t.Fatalf("accepted %+v", o)
		}
	}
	if _, err := Open(nil, db, Options{Domain: "d", Owner: "o"}); err == nil {
		t.Fatal("nil context accepted")
	}
}

func TestClaimCoherentReplayAndCorruption(t *testing.T) {
	e := session.Entry{ID: "1", Seq: 1, Lane: "main", Timestamp: 1, Type: session.EntryTypeCustom, Custom: input("1").Custom}
	raw, _ := json.Marshal(session.LogItem{Kind: session.LogItemEntry, Seq: 1, Entry: &e})
	for _, name := range []string{"valid", "gap", "head mismatch", "invalid record", "invalid header"} {
		t.Run(name, func(t *testing.T) {
			s := claimSteps(time.Second)
			s[3].rows[0][4] = int64(1)
			s[6].rows = [][]driver.Value{{int64(1), raw}}
			switch name {
			case "gap":
				s[6].rows[0][0] = int64(2)
			case "head mismatch":
				s[3].rows[0][4] = int64(2)
			case "invalid record":
				s[6].rows[0][1] = []byte(`{"kind":"lane"}`)
			case "invalid header":
				s[3].rows[0][0] = []byte(`{"kind":"session","version":999,"id":"superagent"}`)
			}
			if name == "valid" {
				j, _ := testJournal(t, s, time.Second)
				if len(j.Log()) != 1 || j.Log()[0].Entry.ID != "1" {
					t.Fatal("replay lost entry")
				}
				return
			}
			if name == "invalid header" {
				s = append(s[:6], step{op: "rollback"})
			} else {
				s = append(s[:7], step{op: "rollback"})
			}
			db, _ := scriptedDB(t, s...)
			j, err := Open(t.Context(), db, Options{Domain: "d", Owner: "o"})
			if j != nil || err == nil {
				t.Fatal("corrupt snapshot accepted")
			}
		})
	}
}
func TestAppendChecksConditionalUpdateCount(t *testing.T) {
	zero := int64(0)
	now := time.Now()
	s := claimSteps(time.Second)
	s = append(s, beginSteps()...)
	s = append(s, rowStep("o", 1, 0, now.Add(time.Second)), clockStep(now), step{op: "exec", sql: "SET seq=", affected: &zero}, step{op: "rollback"})
	j, _ := testJournal(t, s, time.Second)
	if _, err := j.AppendEntry("main", input("1")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("failed CAS accepted: %v", err)
	}
}
