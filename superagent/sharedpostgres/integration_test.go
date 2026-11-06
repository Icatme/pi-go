package sharedpostgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent/session"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// These tests require an explicitly disposable PostgreSQL database. They never
// start a server, discover credentials, or connect unless BOTH gates are set:
//
//	PIGO_POSTGRES_TEST_DSN=postgres://user:password@host:port/database?sslmode=...
//	PIGO_POSTGRES_TEST_DISPOSABLE=1
//
// They create the fixed package schema and remove only their random test domains.
// Tests must not use t.Parallel: configuration parsing has a hermetic environment.
// Independent pools are independent clients, NOT independent hosts/processes.
func postgresFixture(t *testing.T) (*sql.DB, pgx.ConnConfig) {
	t.Helper()
	if os.Getenv("PIGO_POSTGRES_TEST_DISPOSABLE") != "1" || strings.TrimSpace(os.Getenv("PIGO_POSTGRES_TEST_DSN")) == "" {
		t.Skip("real PostgreSQL not tested: require PIGO_POSTGRES_TEST_DSN and PIGO_POSTGRES_TEST_DISPOSABLE=1")
	}
	dsn := os.Getenv("PIGO_POSTGRES_TEST_DSN")
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || u.User == nil || u.User.Username() == "" || strings.Trim(u.Path, "/") == "" {
		t.Fatal("test DSN must be a PostgreSQL URL with explicit host, user and database; value omitted")
	}
	sslmode := u.Query().Get("sslmode")
	ip := net.ParseIP(u.Hostname())
	loopback := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if sslmode != "verify-full" && !(sslmode == "disable" && loopback) {
		t.Fatal("test DSN requires sslmode=verify-full, or explicit sslmode=disable on loopback")
	}
	for key := range u.Query() {
		switch key {
		case "sslmode", "sslrootcert", "sslcert", "sslkey", "connect_timeout":
		default:
			t.Fatalf("unsupported test DSN option %q; no service/passfile or alternate endpoint discovery", key)
		}
	}
	// pgx normally consults PG* and home-directory defaults. Never inherit those
	// when an opt-in test DSN omits an unrelated setting or uses local trust auth.
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(key, "PG") {
			t.Setenv(key, "")
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", home)
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid explicit test PostgreSQL configuration; connection details omitted")
	}
	if len(config.Fallbacks) != 0 {
		t.Fatal("integration tests require one explicit primary endpoint, without fallback hosts")
	}
	config.ConnectTimeout = 5 * time.Second
	config.RuntimeParams["application_name"] = "pi-go-disposable-journal-test"
	config.RuntimeParams["statement_timeout"] = "5000"
	config.RuntimeParams["lock_timeout"] = "2000"
	config.RuntimeParams["idle_in_transaction_session_timeout"] = "5000"
	db := postgresDB(t, *config, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal("explicit disposable PostgreSQL is unavailable; connection details omitted")
	}
	var initialized bool
	if err := db.QueryRowContext(ctx, "SELECT to_regnamespace('pi_superagent_v1') IS NOT NULL").Scan(&initialized); err != nil {
		t.Fatal("could not inspect the disposable journal schema; database details omitted")
	}
	if !initialized {
		if _, err := db.ExecContext(ctx, Schema); err != nil {
			t.Fatal("could not initialize the disposable journal schema; database details omitted")
		}
	}
	// Inspect before any random-domain writes. An existing namespace must have
	// exactly the expected base tables and column types, not views or a partial
	// migration. This is test provisioning, not a runtime schema migration API.
	var tableCount int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM information_schema.tables WHERE table_schema='pi_superagent_v1' AND table_type='BASE TABLE'").Scan(&tableCount); err != nil || tableCount != 2 {
		t.Fatal("unexpected test schema tables; no repair or destructive reset attempted")
	}
	rows, err := db.QueryContext(ctx, "SELECT table_name,column_name,data_type,is_nullable FROM information_schema.columns WHERE table_schema='pi_superagent_v1' ORDER BY table_name,ordinal_position")
	if err != nil {
		t.Fatal("could not inspect test schema columns")
	}
	var columns []string
	for rows.Next() {
		var table, name, kind, nullable string
		if err := rows.Scan(&table, &name, &kind, &nullable); err != nil {
			_ = rows.Close()
			t.Fatal("could not decode test schema columns")
		}
		columns = append(columns, table+"."+name+":"+kind+":"+nullable)
	}
	err = rows.Err()
	_ = rows.Close()
	wantColumns := "domains.domain:text:NO|domains.header:bytea:NO|domains.owner:text:NO|domains.epoch:bigint:NO|domains.expires:timestamp with time zone:NO|domains.seq:bigint:NO|transitions.domain:text:NO|transitions.seq:bigint:NO|transitions.record:bytea:NO"
	if err != nil || strings.Join(columns, "|") != wantColumns {
		t.Fatal("unexpected test schema columns; no repair or destructive reset attempted")
	}
	return db, *config
}

func TestPostgresFixtureRequiresBothGates(t *testing.T) {
	for _, tc := range []struct{ name, dsn, disposable string }{
		{"neither", "", ""},
		{"dsn_only", "invalid-and-must-not-be-parsed", ""},
		{"opt_in_only", "", "1"},
	} {
		var skipped bool
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PIGO_POSTGRES_TEST_DSN", tc.dsn)
			t.Setenv("PIGO_POSTGRES_TEST_DISPOSABLE", tc.disposable)
			defer func() { skipped = t.Skipped() }()
			postgresFixture(t)
		})
		if !skipped {
			t.Errorf("%s did not stop at the configuration gate", tc.name)
		}
	}
}

func postgresDB(t *testing.T, config pgx.ConnConfig, fault *postgresFault) *sql.DB {
	t.Helper()
	connector := stdlib.GetConnector(config)
	if fault != nil {
		connector = postgresFaultConnector{connector, fault}
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(2)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func postgresDomain(t *testing.T, db *sql.DB) string {
	t.Helper()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	domain := "integration-" + hex.EncodeToString(random[:])
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Error("could not begin cleanup of this test domain")
			return
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, "DELETE FROM pi_superagent_v1.transitions WHERE domain=$1", domain); err == nil {
			_, err = tx.ExecContext(ctx, "DELETE FROM pi_superagent_v1.domains WHERE domain=$1", domain)
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			t.Error("could not clean this test domain; no wider schema cleanup attempted")
		}
	})
	return domain
}

func postgresOptions(domain, owner string) Options {
	return Options{Domain: domain, Owner: owner, LeaseTTL: 4 * time.Second, TransactionTimeout: time.Second}
}

func postgresOpen(t *testing.T, db *sql.DB, opts Options) *Journal {
	t.Helper()
	j, err := Open(t.Context(), db, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j
}

func postgresInput(id string) session.NewEntry {
	return session.NewEntry{ID: id, Type: session.EntryTypeCustom, Custom: &session.CustomData{
		CustomType: "superagent.transition", Payload: []byte(`{"event":"integration","integer":9007199254740993}`),
	}}
}

func postgresCounts(t *testing.T, db *sql.DB, domain string) (int64, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var seq int64
	var records int
	if err := db.QueryRowContext(ctx, "SELECT seq, (SELECT count(*) FROM pi_superagent_v1.transitions WHERE domain=$1) FROM pi_superagent_v1.domains WHERE domain=$1", domain).Scan(&seq, &records); err != nil {
		t.Fatal(err)
	}
	return seq, records
}

func TestPostgresIndependentClientsContend(t *testing.T) {
	db, config := postgresFixture(t)
	domain := postgresDomain(t, db)
	db2 := postgresDB(t, config, nil)
	start := make(chan struct{})
	type result struct {
		journal *Journal
		err     error
	}
	results := make(chan result, 2)
	for i, client := range []*sql.DB{db, db2} {
		go func(i int, client *sql.DB) {
			<-start
			owner := "client-a"
			if i == 1 {
				owner = "client-b"
			}
			j, err := Open(t.Context(), client, postgresOptions(domain, owner))
			results <- result{j, err}
		}(i, client)
	}
	close(start)
	var winner *Journal
	busy := 0
	for range 2 {
		select {
		case r := <-results:
			if r.journal != nil {
				t.Cleanup(func() { _ = r.journal.Close() })
				if winner != nil {
					t.Fatal("two clients acquired the same live lease")
				}
				winner = r.journal
			} else if errors.Is(r.err, ErrBusy) {
				busy++
			} else {
				t.Fatalf("unexpected claim result: %v", r.err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent claims did not finish")
		}
	}
	if winner == nil || busy != 1 {
		t.Fatal("expected one owner and one busy contender")
	}
	if _, err := winner.AppendEntry(session.MainLane, postgresInput("1")); err != nil {
		t.Fatal(err)
	}
	epoch := winner.Epoch()
	if err := winner.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := postgresOpen(t, db2, postgresOptions(domain, "replacement"))
	if reopened.Epoch() <= epoch || len(reopened.Log()) != 1 {
		t.Fatal("takeover lost epoch or committed history")
	}
}

func TestPostgresExpiredOwnerCannotRenewAppendOrReleaseReplacement(t *testing.T) {
	db, config := postgresFixture(t)
	domain := postgresDomain(t, db)
	opts := postgresOptions(domain, "reused-instance-name")
	opts.LeaseTTL, opts.TransactionTimeout = 600*time.Millisecond, 150*time.Millisecond
	old := postgresOpen(t, db, opts)
	old.stopRenewal()
	select {
	case <-old.Context().Done():
	case <-time.After(2 * time.Second):
		t.Fatal("lease deadline did not cancel ownership")
	}
	db2 := postgresDB(t, config, nil)
	var replacement *Journal
	deadline := time.Now().Add(3 * time.Second)
	for {
		var err error
		replacement, err = Open(t.Context(), db2, postgresOptions(domain, opts.Owner))
		if err == nil {
			break
		}
		if !errors.Is(err, ErrBusy) || time.Now().After(deadline) {
			t.Fatalf("takeover failed: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() { _ = replacement.Close() })
	if replacement.Epoch() <= old.Epoch() {
		t.Fatal("reused owner name reset epoch")
	}
	if err := old.Renew(); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale renewal: %v", err)
	}
	if _, err := old.AppendEntry(session.MainLane, postgresInput("1")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale append: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := replacement.AppendEntry(session.MainLane, postgresInput("1")); err != nil {
		t.Fatalf("stale Close harmed replacement: %v", err)
	}
	if seq, records := postgresCounts(t, db, domain); seq != 1 || records != 1 {
		t.Fatalf("durable seq=%d records=%d", seq, records)
	}
}

func TestPostgresDatabaseEpochFencesLocallyLiveOwner(t *testing.T) {
	db, config := postgresFixture(t)
	domain := postgresDomain(t, db)
	old := postgresOpen(t, db, Options{Domain: domain, Owner: "same-owner", LeaseTTL: 30 * time.Second, TransactionTimeout: time.Second})
	old.stopRenewal()
	// Only this random fixture's row is expired. The old process still considers
	// itself live, so rejection must come from the database, not its local timer.
	if _, err := db.ExecContext(t.Context(), "UPDATE pi_superagent_v1.domains SET expires=clock_timestamp()-interval '1 second' WHERE domain=$1", domain); err != nil {
		t.Fatal(err)
	}
	replacement := postgresOpen(t, postgresDB(t, config, nil), postgresOptions(domain, "same-owner"))
	if old.Context().Err() != nil {
		t.Fatal("fixture expired locally before testing the database fence")
	}
	if _, err := old.AppendEntry(session.MainLane, postgresInput("1")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("database accepted superseded epoch: %v", err)
	}
	if old.Context().Err() == nil {
		t.Fatal("superseded epoch did not invalidate old handle")
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := replacement.AppendEntry(session.MainLane, postgresInput("1")); err != nil {
		t.Fatal(err)
	}
	if seq, records := postgresCounts(t, db, domain); seq != 1 || records != 1 {
		t.Fatalf("stale decision persisted: seq=%d records=%d", seq, records)
	}
}

func TestPostgresRowLockWaitIsBoundedAndFailsClosed(t *testing.T) {
	db, config := postgresFixture(t)
	domain := postgresDomain(t, db)
	opts := postgresOptions(domain, "locked-owner")
	opts.LeaseTTL, opts.TransactionTimeout = 900*time.Millisecond, 200*time.Millisecond
	j := postgresOpen(t, db, opts)
	j.stopRenewal()
	blocker := postgresDB(t, config, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tx, err := blocker.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var seq int64
	if err := tx.QueryRowContext(ctx, "SELECT seq FROM pi_superagent_v1.domains WHERE domain=$1 FOR UPDATE", domain).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := j.AppendEntry(session.MainLane, postgresInput("1")); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("append passed a held domain lock")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("locked append exceeded its transaction/deadline bounds")
	}
	if j.Context().Err() == nil {
		t.Fatal("lock timeout did not invalidate ownership")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if seq, records := postgresCounts(t, db, domain); seq != 0 || records != 0 {
		t.Fatalf("blocked append persisted seq=%d records=%d", seq, records)
	}
}

func TestPostgresClockIsCheckedAfterDomainLockWait(t *testing.T) {
	db, config := postgresFixture(t)
	domain := postgresDomain(t, db)
	fault := &postgresFault{lockStarted: make(chan struct{})}
	j := postgresOpen(t, postgresDB(t, config, fault), Options{Domain: domain, Owner: "clock-owner", LeaseTTL: 30 * time.Second, TransactionTimeout: 5 * time.Second})
	j.stopRenewal()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var seq int64
	if err := tx.QueryRowContext(ctx, "SELECT seq FROM pi_superagent_v1.domains WHERE domain=$1 FOR UPDATE", domain).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	fault.signalLock.Store(true)
	done := make(chan error, 1)
	go func() { _, err := j.AppendEntry(session.MainLane, postgresInput("1")); done <- err }()
	select {
	case <-fault.lockStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("append did not reach the domain lock")
	}
	// Set expiry after the append transaction has begun, then release the row
	// only after expiry. A transaction-start now() would incorrectly look live.
	if _, err := tx.ExecContext(ctx, "UPDATE pi_superagent_v1.domains SET expires=clock_timestamp()+interval '100 milliseconds' WHERE domain=$1", domain); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("post-lock expiry was not rejected: %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("append did not finish after releasing the lock")
	}
	if seq, records := postgresCounts(t, db, domain); seq != 0 || records != 0 {
		t.Fatalf("expired decision persisted: seq=%d records=%d", seq, records)
	}
}

// The fault connector still executes transactions on PostgreSQL. It injects a
// real server-side division-by-zero abort immediately before the record insert,
// or drops only the client's successful Commit result. Neither is advertised as
// an actual severed TCP connection, server crash, or cross-host partition.
type postgresFault struct {
	abortInsert, loseCommitAck, signalLock atomic.Bool
	lockStarted                            chan struct{}
}
type postgresFaultConnector struct {
	driver.Connector
	fault *postgresFault
}

func (c postgresFaultConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	pgconn, ok := conn.(*stdlib.Conn)
	if !ok {
		_ = conn.Close()
		return nil, errors.New("integration connector requires pgx stdlib")
	}
	return &postgresFaultConn{Conn: pgconn, fault: c.fault}, nil
}

type postgresFaultConn struct {
	*stdlib.Conn
	fault *postgresFault
}

func (c *postgresFaultConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return postgresFaultTx{Tx: tx, fault: c.fault}, nil
}
func (c *postgresFaultConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "INSERT INTO pi_superagent_v1.transitions") && c.fault.abortInsert.CompareAndSwap(true, false) {
		return c.Conn.ExecContext(ctx, "SELECT 1/0", nil)
	}
	return c.Conn.ExecContext(ctx, query, args)
}
func (c *postgresFaultConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "FOR UPDATE") && c.fault.signalLock.CompareAndSwap(true, false) {
		close(c.fault.lockStarted)
	}
	return c.Conn.QueryContext(ctx, query, args)
}

type postgresFaultTx struct {
	driver.Tx
	fault *postgresFault
}

func (tx postgresFaultTx) Commit() error {
	if err := tx.Tx.Commit(); err != nil {
		return err
	}
	if tx.fault.loseCommitAck.CompareAndSwap(true, false) {
		return errors.New("injected lost commit acknowledgement")
	}
	return nil
}

func TestPostgresServerAbortRollsBackAppend(t *testing.T) {
	db, config := postgresFixture(t)
	domain := postgresDomain(t, db)
	fault := &postgresFault{}
	j := postgresOpen(t, postgresDB(t, config, fault), postgresOptions(domain, "aborted"))
	j.stopRenewal()
	fault.abortInsert.Store(true)
	if _, err := j.AppendEntry(session.MainLane, postgresInput("1")); err == nil {
		t.Fatal("server-aborted append succeeded")
	}
	if fault.abortInsert.Load() {
		t.Fatal("transaction abort was not injected")
	}
	if len(j.Log()) != 0 || j.Context().Err() == nil {
		t.Fatal("aborted append changed cache or retained ownership")
	}
	if seq, records := postgresCounts(t, db, domain); seq != 0 || records != 0 {
		t.Fatalf("partial transaction seq=%d records=%d", seq, records)
	}
}

func TestPostgresLostCommitAcknowledgementRequiresReplay(t *testing.T) {
	db, config := postgresFixture(t)
	domain := postgresDomain(t, db)
	fault := &postgresFault{}
	j := postgresOpen(t, postgresDB(t, config, fault), postgresOptions(domain, "unknown"))
	j.stopRenewal()
	fault.loseCommitAck.Store(true)
	if _, err := j.AppendEntry(session.MainLane, postgresInput("1")); !errors.Is(err, ErrCommitUnknown) {
		t.Fatalf("lost acknowledgement: %v", err)
	}
	if fault.loseCommitAck.Load() {
		t.Fatal("commit acknowledgement fault was not reached")
	}
	if len(j.Log()) != 0 || j.Context().Err() == nil {
		t.Fatal("unknown result was adopted or ownership retained")
	}
	if _, err := j.AppendEntry(session.MainLane, postgresInput("1")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("unknown append retried: %v", err)
	}
	if seq, records := postgresCounts(t, db, domain); seq != 1 || records != 1 {
		t.Fatalf("committed fact missing or duplicated: seq=%d records=%d", seq, records)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := postgresOpen(t, db, postgresOptions(domain, "replay"))
	if len(reopened.Log()) != 1 {
		t.Fatal("replay did not recover uncertain committed record")
	}
	if _, err := reopened.AppendEntry(session.MainLane, postgresInput("2")); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresJSONLImportIsAtomicAndPreservesBytes(t *testing.T) {
	db, config := postgresFixture(t)
	domain := postgresDomain(t, db)
	source := filepath.Join(t.TempDir(), "legacy.jsonl")
	legacy, err := session.CreateJSONLStorage(source, session.Header{ID: "superagent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = legacy.AppendEntry(session.MainLane, postgresInput("1")); err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	branch, err := legacy.ReadBranch(session.MainLane)
	if err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	if _, _, err = legacy.CompareAppend(branch.Version, []session.NewEntry{postgresInput("2"), postgresInput("3")}); err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	if err = legacy.Close(); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(original, []byte{'\n'}), []byte{'\n'})
	var batch struct {
		Items []json.RawMessage `json:"items"`
	}
	if err = json.Unmarshal(lines[2], &batch); err != nil || len(batch.Items) != 2 {
		t.Fatal("unexpected legacy batch fixture")
	}
	fault := &postgresFault{}
	fault.abortInsert.Store(true)
	if err = ImportJSONL(t.Context(), postgresDB(t, config, fault), domain, source); err == nil {
		t.Fatal("server-aborted import succeeded")
	}
	if fault.abortInsert.Load() {
		t.Fatal("import abort was not injected")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var count int
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM pi_superagent_v1.domains WHERE domain=$1", domain).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial domain import: count=%d error=%v", count, err)
	}
	if err = ImportJSONL(t.Context(), db, domain, source); err != nil {
		t.Fatal(err)
	}
	if err = ImportJSONL(t.Context(), db, domain, source); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate import: %v", err)
	}
	var header []byte
	if err = db.QueryRowContext(ctx, "SELECT header FROM pi_superagent_v1.domains WHERE domain=$1", domain).Scan(&header); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(header, lines[0]) {
		t.Fatal("header bytes changed")
	}
	for i, want := range [][]byte{lines[1], batch.Items[0], batch.Items[1]} {
		var record []byte
		if err = db.QueryRowContext(ctx, "SELECT record FROM pi_superagent_v1.transitions WHERE domain=$1 AND seq=$2", domain, i+1).Scan(&record); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(record, want) {
			t.Fatalf("record %d bytes changed", i+1)
		}
	}
	after, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("import modified source")
	}
	j := postgresOpen(t, db, postgresOptions(domain, "import-reader"))
	if len(j.Log()) != 3 {
		t.Fatal("import did not replay all batch entries")
	}
	if _, err = j.AppendEntry(session.MainLane, postgresInput("4")); err != nil {
		t.Fatal(err)
	}
}
