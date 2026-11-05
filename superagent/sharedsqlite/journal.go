// Package sharedsqlite provides a same-host, local-filesystem journal and a
// coarse authorization-domain lease. It is not a distributed database. All
// writers must use this protocol; external effects are not fenced by SQLite.
// Network filesystems and multi-host access are unsupported. Supported targets
// are Linux, macOS and Windows on amd64/arm64. Use a trusted private directory;
// Windows callers must secure its ACL (POSIX mode bits do not enforce ACLs).
package sharedsqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Icatme/pi-go/agent/session"
	_ "github.com/ncruces/go-sqlite3/driver"
)

var (
	ErrBusy      = errors.New("sharedsqlite: domain lease is busy")
	ErrLeaseLost = errors.New("sharedsqlite: lease lost; reopen and replay before writing")
	ErrExists    = errors.New("sharedsqlite: destination domain already exists")
)

const dbNow = "CAST(unixepoch('subsec') * 1000 AS INTEGER)"
const schema = `CREATE TABLE domains (domain TEXT PRIMARY KEY, header BLOB NOT NULL, owner TEXT NOT NULL, epoch INTEGER NOT NULL CHECK(epoch >= 0), expires INTEGER NOT NULL, seq INTEGER NOT NULL CHECK(seq >= 0)) STRICT;
CREATE TABLE transitions (domain TEXT NOT NULL REFERENCES domains(domain), seq INTEGER NOT NULL, record BLOB NOT NULL, PRIMARY KEY(domain,seq)) STRICT;
PRAGMA user_version=1;`

type Options struct {
	Domain, Owner string
	LeaseTTL      time.Duration
}

// Journal owns one domain until Close or Context cancellation. Owner is an
// instance identity, not an authentication mechanism. Epoch fences old owners.
type Journal struct {
	mu       sync.Mutex
	db       *sql.DB
	opts     Options
	epoch    int64
	header   session.Header
	log      []session.LogItem
	ctx      context.Context
	cancel   context.CancelFunc
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	closed   bool
}

func connect(ctx context.Context, path string) (*sql.DB, error) {
	if !supportedLocking {
		return nil, errors.New("sharedsqlite: unsupported locking build tag sqlite3_dotlk")
	}
	if (runtime.GOOS != "linux" && runtime.GOOS != "darwin" && runtime.GOOS != "windows") || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return nil, errors.New("sharedsqlite: unsupported platform")
	}
	if strings.TrimSpace(path) == "" || path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil, errors.New("sharedsqlite: a local file path is required, not a DSN")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	parent, err := os.Stat(filepath.Dir(abs))
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" && parent.Mode().Perm()&0077 != 0 {
		return nil, errors.New("sharedsqlite: database directory must be private (0700)")
	}
	// The containing directory must be trusted: path replacement by another
	// actor is outside the lease protocol. Reject exposed or special files.
	f, createErr := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if createErr == nil {
		if err := f.Close(); err != nil {
			return nil, err
		}
	} else if !errors.Is(createErr, os.ErrExist) {
		return nil, createErr
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, errors.New("sharedsqlite: database must be a private regular file (0600)")
	}
	p := filepath.ToSlash(abs)
	if runtime.GOOS == "windows" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	q := url.Values{}
	q.Set("mode", "rwc")
	q.Set("modeof", abs)
	q.Add("_pragma", "busy_timeout(1000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(FULL)")
	// SQLite URI parameters use percent escaping, not HTML form plus escaping.
	u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = initialize(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func begin(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	return db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
}
func initialize(ctx context.Context, db *sql.DB) error {
	tx, err := begin(ctx, db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var v int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	switch v {
	case 0:
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("sharedsqlite: unrecognized database schema")
		}
		if _, err = tx.ExecContext(ctx, schema); err != nil {
			return err
		}
	case 1:
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").Scan(&count); err != nil {
			return err
		}
		if count != 2 {
			return errors.New("sharedsqlite: unexpected schema objects")
		}
		// Require the exact schema, rather than accepting a version marker alone.
		for name, want := range map[string]string{"domains": strings.Split(schema, ";\n")[0], "transitions": strings.Split(schema, ";\n")[1]} {
			var got string
			if err = tx.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&got); err != nil {
				return err
			}
			if got != want {
				return errors.New("sharedsqlite: incompatible schema")
			}
		}
	default:
		return fmt.Errorf("sharedsqlite: unsupported schema version %d", v)
	}
	return tx.Commit()
}
func Open(ctx context.Context, path string, opts Options) (*Journal, error) {
	if strings.TrimSpace(opts.Domain) == "" || strings.TrimSpace(opts.Owner) == "" {
		return nil, errors.New("sharedsqlite: domain and owner are required")
	}
	if opts.LeaseTTL == 0 {
		opts.LeaseTTL = 30 * time.Second
	}
	if opts.LeaseTTL < 30*time.Millisecond || opts.LeaseTTL > 24*time.Hour {
		return nil, errors.New("sharedsqlite: lease TTL must be between 30ms and 24h")
	}
	db, err := connect(ctx, path)
	if err != nil {
		return nil, err
	}
	j := &Journal{db: db, opts: opts, stop: make(chan struct{}), done: make(chan struct{})}
	j.ctx, j.cancel = context.WithCancel(ctx)
	if err = j.claim(ctx); err != nil {
		j.cancel()
		db.Close()
		return nil, err
	}
	go j.renewLoop()
	return j, nil
}
func (j *Journal) claim(ctx context.Context) error {
	tx, err := begin(ctx, j.db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	header := session.Header{Kind: session.HeaderKindSession, Version: session.CurrentFormatVersion, ID: "superagent", CreatedAt: time.Now().UnixMilli()}
	raw, _ := json.Marshal(header)
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO domains VALUES(?,?, '',0,0,0)", j.opts.Domain, raw); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE domains SET owner=?,epoch=epoch+1,expires="+dbNow+"+? WHERE domain=? AND expires<="+dbNow+" AND epoch<9223372036854775807", j.opts.Owner, j.opts.LeaseTTL.Milliseconds(), j.opts.Domain)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrBusy
	}
	var seq int64
	if err = tx.QueryRowContext(ctx, "SELECT header,epoch,seq FROM domains WHERE domain=?", j.opts.Domain).Scan(&raw, &j.epoch, &seq); err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &j.header); err != nil {
		return err
	}
	if err = validHeader(j.header); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT seq,record FROM transitions WHERE domain=? ORDER BY seq", j.opts.Domain)
	if err != nil {
		return err
	}
	for rows.Next() {
		var s int64
		var b []byte
		if err = rows.Scan(&s, &b); err != nil {
			rows.Close()
			return err
		}
		var item session.LogItem
		if err = json.Unmarshal(b, &item); err != nil {
			rows.Close()
			return err
		}
		if s != int64(len(j.log)+1) {
			rows.Close()
			return errors.New("sharedsqlite: inconsistent journal sequence")
		}
		if err = validItem(item, uint64(s)); err != nil {
			rows.Close()
			return err
		}
		j.log = append(j.log, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if seq != int64(len(j.log)) {
		return errors.New("sharedsqlite: journal head mismatch")
	}
	return tx.Commit()
}
func validHeader(h session.Header) error {
	if h.Kind != session.HeaderKindSession || h.Version != session.CurrentFormatVersion || h.ID != "superagent" || h.ParentSessionID != "" || h.CreatedAt < 0 {
		return errors.New("sharedsqlite: invalid superagent header")
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
		return errors.New("sharedsqlite: expected sequential main-lane superagent transition")
	}
	return nil
}
func clone[T any](v T) T {
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}
func (j *Journal) Header() session.Header   { j.mu.Lock(); defer j.mu.Unlock(); return j.header }
func (j *Journal) Log() []session.LogItem   { j.mu.Lock(); defer j.mu.Unlock(); return clone(j.log) }
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
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed || j.ctx.Err() != nil {
		return ErrLeaseLost
	}
	defer func() {
		if err != nil {
			j.cancel()
		}
	}()
	tx, err := begin(j.ctx, j.db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(j.ctx, "UPDATE domains SET expires="+dbNow+"+? WHERE domain=? AND owner=? AND epoch=? AND expires>"+dbNow, j.opts.LeaseTTL.Milliseconds(), j.opts.Domain, j.opts.Owner, j.epoch)
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
	return tx.Commit()
}
func (j *Journal) AppendEntry(lane string, input session.NewEntry) (entry session.Entry, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed || j.ctx.Err() != nil {
		return entry, ErrLeaseLost
	}
	seq := uint64(len(j.log) + 1)
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
	// Any failure after attempting the transaction is fail-closed: even an
	// uncertain commit must be resolved by reopening and replaying the journal.
	defer func() {
		if err != nil {
			j.cancel()
		}
	}()
	tx, err := begin(j.ctx, j.db)
	if err != nil {
		return session.Entry{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(j.ctx, "UPDATE domains SET seq=? WHERE domain=? AND owner=? AND epoch=? AND expires>"+dbNow+" AND seq=?", seq, j.opts.Domain, j.opts.Owner, j.epoch, seq-1)
	if err != nil {
		return session.Entry{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return session.Entry{}, err
	}
	if n != 1 {
		return session.Entry{}, ErrLeaseLost
	}
	if _, err = tx.ExecContext(j.ctx, "INSERT INTO transitions VALUES(?,?,?)", j.opts.Domain, seq, raw); err != nil {
		return session.Entry{}, err
	}
	// Recheck expiry after the insert too, immediately before commit. The
	// write lock excludes any takeover throughout this transaction.
	var live int
	if err = tx.QueryRowContext(j.ctx, "SELECT count(*) FROM domains WHERE domain=? AND owner=? AND epoch=? AND expires>"+dbNow, j.opts.Domain, j.opts.Owner, j.epoch).Scan(&live); err != nil {
		return session.Entry{}, err
	}
	if live != 1 {
		return session.Entry{}, ErrLeaseLost
	}
	if err = tx.Commit(); err != nil {
		return session.Entry{}, err
	}
	j.log = append(j.log, item)
	return clone(*item.Entry), nil
}
func (j *Journal) Close() error {
	j.cancel()
	j.stopRenewal()
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Preserve the durable epoch even when releasing the current ownership.
	_, err := j.db.ExecContext(ctx, "UPDATE domains SET owner='',expires=0 WHERE domain=? AND owner=? AND epoch=?", j.opts.Domain, j.opts.Owner, j.epoch)
	return errors.Join(err, j.db.Close())
}
