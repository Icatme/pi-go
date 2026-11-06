// Package sharedpostgres provides an optional single-primary PostgreSQL journal
// and coarse authorization-domain lease. Hosts own the database, driver, TLS,
// authentication and schema migration. No credentials or environment variables
// are read here. All writers must follow this protocol. Database fencing cannot
// undo external effects or make them exactly-once. See README.md for limits.
package sharedpostgres

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Icatme/pi-go/agent/session"
)

// Schema is applied explicitly by the trusted host, not by Open or ImportJSONL.
// Use a dedicated schema controlled by the host; runtime writers need no DDL.
//
//go:embed schema.sql
var Schema string

var (
	ErrBusy          = errors.New("sharedpostgres: domain lease is busy")
	ErrLeaseLost     = errors.New("sharedpostgres: lease lost; reopen and replay before writing")
	ErrExists        = errors.New("sharedpostgres: destination domain already exists")
	ErrCommitUnknown = errors.New("sharedpostgres: commit outcome unknown; inspect durable state before retrying")
)

type Options struct {
	Domain, Owner string
	LeaseTTL      time.Duration
	// TransactionTimeout covers pool/row-lock waits, statements and COMMIT.
	// Drivers must honor context cancellation. Defaults to min(5s, LeaseTTL/4).
	TransactionTimeout time.Duration
}

// Journal is one leased snapshot. Close releases only this epoch and never
// closes the caller-owned *sql.DB. Epoch is immutable for this handle. A lost
// handle cannot be renewed or reused, including after an uncertain COMMIT.
type Journal struct {
	db         *sql.DB
	opts       Options
	epoch      int64
	ctx        context.Context
	cancel     context.CancelFunc
	gate       chan struct{} // serializes transactions; never protects the deadline
	mu         sync.Mutex    // only short, in-memory operations; no database I/O
	header     session.Header
	log        []session.LogItem
	deadline   time.Time // conservative local monotonic deadline
	timer      *time.Timer
	generation uint64
	closed     bool
	stop       chan struct{}
	done       chan struct{}
	stopOnce   sync.Once
}

func options(o Options) (Options, error) {
	if strings.TrimSpace(o.Domain) == "" || strings.TrimSpace(o.Owner) == "" {
		return o, errors.New("sharedpostgres: domain and owner are required")
	}
	if o.LeaseTTL == 0 {
		o.LeaseTTL = 30 * time.Second
	}
	if o.LeaseTTL < 300*time.Millisecond || o.LeaseTTL > 24*time.Hour {
		return o, errors.New("sharedpostgres: lease TTL must be between 300ms and 24h")
	}
	if o.TransactionTimeout == 0 {
		o.TransactionTimeout = min(5*time.Second, o.LeaseTTL/4)
	}
	if o.TransactionTimeout < time.Millisecond || o.TransactionTimeout > o.LeaseTTL/3 {
		return o, errors.New("sharedpostgres: transaction timeout must be between 1ms and one third of the TTL")
	}
	return o, nil
}

// Open claims a domain and replays its entire journal in one bounded transaction.
// The host must provision Schema first and supply a single authoritative primary
// with durable commits. Replica routing and automatic failover are unsupported.
func Open(ctx context.Context, db *sql.DB, opts Options) (*Journal, error) {
	if ctx == nil || db == nil {
		return nil, errors.New("sharedpostgres: context and database are required")
	}
	opts, err := options(opts)
	if err != nil {
		return nil, err
	}
	j := &Journal{db: db, opts: opts, gate: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	j.ctx, j.cancel = context.WithCancel(ctx)
	started := time.Now()
	if err = j.claim(started); err != nil {
		j.cancel()
		return nil, err
	}
	if err = j.confirmDeadline(started.Add(opts.LeaseTTL)); err != nil {
		j.cancel()
		return nil, err
	}
	go j.renewLoop()
	return j, nil
}

// Each transaction has both a client deadline and transaction-local server
// limits. synchronous_commit must not be inherited as off from a pooled session.
// Trusted host settings (fsync, replication, driver behavior) still matter.
func begin(ctx context.Context, db *sql.DB, timeout time.Duration) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `SELECT set_config('statement_timeout',$1,true), set_config('lock_timeout',$1,true), set_config('idle_in_transaction_session_timeout',$1,true), set_config('synchronous_commit','on',true)`, strconv.FormatInt(timeout.Milliseconds(), 10)+"ms")
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

type domainRow struct {
	header    []byte
	owner     string
	epoch     int64
	expires   time.Time
	seq       int64
	checkedAt time.Time
}

func lockDomain(ctx context.Context, tx *sql.Tx, domain string) (domainRow, error) {
	var r domainRow
	err := tx.QueryRowContext(ctx, `SELECT header,owner,epoch,expires,seq FROM pi_superagent_v1.domains WHERE domain=$1 FOR UPDATE`, domain).Scan(&r.header, &r.owner, &r.epoch, &r.expires, &r.seq)
	return r, err
}

// This MUST be a separate statement after FOR UPDATE returns. PostgreSQL now()
// and CURRENT_TIMESTAMP are transaction-start timestamps; neither is a lease clock.
func databaseNow(ctx context.Context, tx *sql.Tx) (time.Time, error) {
	var now time.Time
	err := tx.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now, err
}
func changed(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrLeaseLost
	}
	return nil
}
func commit(ctx context.Context, tx *sql.Tx) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Do not retry or guess whether a driver error proves rollback. A lost ACK
	// can conceal a committed epoch or transition; only a fresh replay resolves it.
	if err := tx.Commit(); err != nil {
		return errors.Join(ErrCommitUnknown, err)
	}
	if err := ctx.Err(); err != nil {
		// A non-cooperative driver can return success after the transaction's
		// deadline. Treat the caller's outcome as may-have-committed too.
		return errors.Join(ErrCommitUnknown, err)
	}
	return nil
}
func beforeCommit(ctx context.Context, tx *sql.Tx, expires, deadline time.Time) error {
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return err
	}
	if !now.Before(expires) || !time.Now().Before(deadline) {
		return ErrLeaseLost
	}
	return ctx.Err()
}
func (j *Journal) claim(started time.Time) error {
	ctx, cancel := context.WithTimeout(j.ctx, j.opts.TransactionTimeout)
	defer cancel()
	tx, err := begin(ctx, j.db, j.opts.TransactionTimeout)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	header := session.Header{Kind: session.HeaderKindSession, Version: session.CurrentFormatVersion, ID: "superagent", CreatedAt: time.Now().UnixMilli()}
	raw, _ := json.Marshal(header)
	if _, err = tx.ExecContext(ctx, `INSERT INTO pi_superagent_v1.domains (domain,header,owner,epoch,expires,seq) VALUES ($1,$2,'',0,'epoch'::timestamptz,0) ON CONFLICT (domain) DO NOTHING`, j.opts.Domain, raw); err != nil {
		return err
	}
	r, err := lockDomain(ctx, tx, j.opts.Domain)
	if err != nil {
		return err
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return err
	}
	if now.Before(r.expires) {
		return ErrBusy
	}
	if r.epoch < 0 || r.epoch == math.MaxInt64 || r.seq < 0 {
		return errors.New("sharedpostgres: invalid or exhausted domain counters")
	}
	j.epoch = r.epoch + 1
	expires := now.Add(j.opts.LeaseTTL)
	if err = changed(tx.ExecContext(ctx, `UPDATE pi_superagent_v1.domains SET owner=$2,epoch=$3,expires=$4 WHERE domain=$1 AND epoch=$5 AND seq=$6`, j.opts.Domain, j.opts.Owner, j.epoch, expires, r.epoch, r.seq)); err != nil {
		return err
	}
	if err = json.Unmarshal(r.header, &j.header); err != nil {
		return err
	}
	if err = validHeader(j.header); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT seq,record FROM pi_superagent_v1.transitions WHERE domain=$1 ORDER BY seq`, j.opts.Domain)
	if err != nil {
		return err
	}
	for rows.Next() {
		var seq int64
		var raw []byte
		if err = rows.Scan(&seq, &raw); err != nil {
			rows.Close()
			return err
		}
		var item session.LogItem
		if err = json.Unmarshal(raw, &item); err != nil {
			rows.Close()
			return err
		}
		if seq != int64(len(j.log))+1 {
			rows.Close()
			return errors.New("sharedpostgres: inconsistent journal sequence")
		}
		if err = validItem(item, uint64(seq)); err != nil {
			rows.Close()
			return err
		}
		j.log = append(j.log, item)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	if r.seq != int64(len(j.log)) {
		return errors.New("sharedpostgres: journal head mismatch")
	}
	if err = beforeCommit(ctx, tx, expires, started.Add(j.opts.LeaseTTL)); err != nil {
		return err
	}
	return commit(ctx, tx)
}

// Renewal receipts never grant TTL measured from receipt time. The local bound
// starts BEFORE waiting for the pool, serialization gate or row lock. A timer
// uses only mu, not gate, so stalled drivers cannot keep callbacks alive.
func (j *Journal) confirmDeadline(deadline time.Time) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now()
	if j.closed || j.ctx.Err() != nil || !now.Before(deadline) || (!j.deadline.IsZero() && !now.Before(j.deadline)) {
		j.cancel()
		return ErrLeaseLost
	}
	j.deadline = deadline
	j.generation++
	generation := j.generation
	if j.timer != nil {
		j.timer.Stop()
	}
	j.timer = time.AfterFunc(time.Until(deadline), func() {
		j.mu.Lock()
		defer j.mu.Unlock()
		if j.generation == generation {
			j.cancel()
		}
	})
	return nil
}
func (j *Journal) acquire(ctx context.Context) error {
	select {
	case j.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-j.gate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (j *Journal) release() { <-j.gate }
func (j *Journal) live() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed || j.ctx.Err() != nil || !time.Now().Before(j.deadline) {
		j.cancel()
		return false
	}
	return true
}
func (j *Journal) verify(ctx context.Context, tx *sql.Tx, seq int64) (domainRow, error) {
	r, err := lockDomain(ctx, tx, j.opts.Domain)
	if err != nil {
		return r, err
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return r, err
	}
	if r.owner != j.opts.Owner || r.epoch != j.epoch || r.seq != seq || !now.Before(r.expires) || !j.live() {
		return r, ErrLeaseLost
	}
	r.checkedAt = now
	return r, nil
}
func (j *Journal) Header() session.Header { j.mu.Lock(); defer j.mu.Unlock(); return j.header }
func (j *Journal) Log() []session.LogItem {
	// Stored entries are immutable. Clone outside the deadline mutex so JSON
	// encoding a large snapshot cannot delay cancellation of ownership.
	j.mu.Lock()
	snapshot := append([]session.LogItem(nil), j.log...)
	j.mu.Unlock()
	return clone(snapshot)
}
func (j *Journal) Epoch() int64             { return j.epoch }
func (j *Journal) Context() context.Context { return j.ctx }
func (j *Journal) stopRenewal()             { j.stopOnce.Do(func() { close(j.stop) }); <-j.done }
func (j *Journal) renewLoop() {
	defer close(j.done)
	tick := time.NewTicker(j.opts.LeaseTTL / 3)
	defer tick.Stop()
	for {
		select {
		case <-j.stop:
			return
		case <-j.ctx.Done():
			return
		case <-tick.C:
			if j.Renew() != nil {
				return
			}
		}
	}
}
func (j *Journal) Renew() (err error) {
	started := time.Now()
	if !j.live() {
		return ErrLeaseLost
	}
	defer func() {
		if err != nil {
			j.cancel()
		}
	}()
	ctx, cancel := context.WithTimeout(j.ctx, j.opts.TransactionTimeout)
	defer cancel()
	if err = j.acquire(ctx); err != nil {
		return err
	}
	defer j.release()
	if !j.live() {
		return ErrLeaseLost
	}
	j.mu.Lock()
	seq := int64(len(j.log))
	oldDeadline := j.deadline
	j.mu.Unlock()
	tx, err := begin(ctx, j.db, j.opts.TransactionTimeout)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := j.verify(ctx, tx, seq)
	if err != nil {
		return err
	}
	expires := r.checkedAt.Add(j.opts.LeaseTTL)
	if err = changed(tx.ExecContext(ctx, `UPDATE pi_superagent_v1.domains SET expires=$4 WHERE domain=$1 AND owner=$2 AND epoch=$3 AND seq=$5`, j.opts.Domain, j.opts.Owner, j.epoch, expires, r.seq)); err != nil {
		return err
	}
	deadline := started.Add(j.opts.LeaseTTL)
	// An extension must finish while the ORIGINAL grant is live. Otherwise a
	// slow renewal could revive its own expired epoch while holding the lock.
	if err = beforeCommit(ctx, tx, minTime(expires, r.expires), minTime(deadline, oldDeadline)); err != nil {
		return err
	}
	if err = commit(ctx, tx); err != nil {
		return err
	}
	return j.confirmDeadline(deadline)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func (j *Journal) AppendEntry(lane string, input session.NewEntry) (entry session.Entry, err error) {
	if !j.live() {
		return entry, ErrLeaseLost
	}
	ctx, cancel := context.WithTimeout(j.ctx, j.opts.TransactionTimeout)
	defer cancel()
	if err = j.acquire(ctx); err != nil {
		j.cancel()
		return entry, err
	}
	defer j.release()
	if !j.live() {
		return entry, ErrLeaseLost
	}
	j.mu.Lock()
	seq := uint64(len(j.log)) + 1
	deadline := j.deadline
	j.mu.Unlock()
	if seq > math.MaxInt64 {
		return entry, errors.New("sharedpostgres: journal sequence exhausted")
	}
	entry = session.Entry{Type: input.Type, ID: input.ID, Seq: seq, Lane: lane, Timestamp: time.Now().UnixMilli(), Message: input.Message, Compaction: input.Compaction, Custom: input.Custom}
	if seq > 1 {
		entry.ParentID = strconv.FormatUint(seq-1, 10)
	}
	item := session.LogItem{Kind: session.LogItemEntry, Seq: seq, Entry: &entry}
	if err = validItem(item, seq); err != nil {
		return session.Entry{}, err
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return session.Entry{}, err
	}
	item = clone(item)
	defer func() {
		if err != nil {
			j.cancel()
		}
	}()
	tx, err := begin(ctx, j.db, j.opts.TransactionTimeout)
	if err != nil {
		return session.Entry{}, err
	}
	defer tx.Rollback()
	r, err := j.verify(ctx, tx, int64(seq-1))
	if err != nil {
		return session.Entry{}, err
	}
	if err = changed(tx.ExecContext(ctx, `UPDATE pi_superagent_v1.domains SET seq=$4 WHERE domain=$1 AND owner=$2 AND epoch=$3 AND seq=$5`, j.opts.Domain, j.opts.Owner, j.epoch, int64(seq), int64(seq-1))); err != nil {
		return session.Entry{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO pi_superagent_v1.transitions (domain,seq,record) VALUES ($1,$2,$3)`, j.opts.Domain, int64(seq), raw); err != nil {
		return session.Entry{}, err
	}
	if err = beforeCommit(ctx, tx, r.expires, deadline); err != nil {
		return session.Entry{}, err
	}
	if err = commit(ctx, tx); err != nil {
		return session.Entry{}, err
	}
	// If cancellation/deadline won the race with COMMIT, do not hand out a
	// successful decision or update the cache. Reopen/replay resolves the write.
	if !j.live() {
		return session.Entry{}, ErrLeaseLost
	}
	j.mu.Lock()
	j.log = append(j.log, item)
	j.mu.Unlock()
	return clone(*item.Entry), nil
}
func (j *Journal) Close() error {
	j.cancel()
	j.stopOnce.Do(func() { close(j.stop) })
	j.mu.Lock()
	if j.closed {
		j.mu.Unlock()
		return nil
	}
	j.closed = true
	if j.timer != nil {
		j.timer.Stop()
	}
	j.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), j.opts.TransactionTimeout)
	defer cancel()
	if err := j.acquire(ctx); err != nil {
		return err
	}
	defer j.release()
	tx, err := begin(ctx, j.db, j.opts.TransactionTimeout)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := lockDomain(ctx, tx, j.opts.Domain)
	if err != nil {
		return err
	}
	if r.owner != j.opts.Owner || r.epoch != j.epoch {
		return nil
	}
	if err = changed(tx.ExecContext(ctx, `UPDATE pi_superagent_v1.domains SET owner='',expires='epoch'::timestamptz WHERE domain=$1 AND owner=$2 AND epoch=$3`, j.opts.Domain, j.opts.Owner, j.epoch)); err != nil {
		return err
	}
	return commit(ctx, tx)
}

func validHeader(h session.Header) error {
	if h.Kind != session.HeaderKindSession || h.Version != session.CurrentFormatVersion || h.ID != "superagent" || h.ParentSessionID != "" || h.CreatedAt < 0 {
		return errors.New("sharedpostgres: invalid superagent header")
	}
	return nil
}
func validItem(v session.LogItem, seq uint64) error {
	e := v.Entry
	parent := ""
	if seq > 1 {
		parent = strconv.FormatUint(seq-1, 10)
	}
	if v.Kind != session.LogItemEntry || v.Lane != nil || e == nil || v.Seq != seq || e.Seq != seq || e.ID != strconv.FormatUint(seq, 10) || e.ParentID != parent || e.Lane != session.MainLane || e.Type != session.EntryTypeCustom || e.Message != nil || e.Compaction != nil || e.Custom == nil || e.Custom.CustomType != "superagent.transition" || e.Timestamp < 0 || (len(e.Custom.Payload) > 0 && !json.Valid(e.Custom.Payload)) {
		return errors.New("sharedpostgres: expected sequential main-lane superagent transition")
	}
	return nil
}
func clone[T any](v T) T {
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}
