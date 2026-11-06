package sharedpostgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/Icatme/pi-go/agent/session"
)

// ImportJSONL copies a stopped legacy journal into an ABSENT destination domain.
// The caller must keep old writers stopped through the entire cutover. The
// source is validated under the existing writer lock, never repaired or changed.
// Header/LogItem JSON bytes are preserved, excluding newlines and batch wrappers.
// Import is one bounded transaction. An uncertain COMMIT must be reconciled by
// reading the destination, not by retrying import or restarting the old writer.
// The host must install Schema first. db remains owned by the caller.
func ImportJSONL(ctx context.Context, db *sql.DB, domain, source string) error {
	if ctx == nil || db == nil || strings.TrimSpace(domain) == "" {
		return errors.New("sharedpostgres: context, database and domain are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Source quiescence is a host precondition, not an online migration protocol.
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("sharedpostgres: source must be a regular file, not a symlink or device")
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return errors.New("sharedpostgres: import requires complete newline-terminated records")
	}
	header, log, err := session.ReadJSONLSnapshot(source)
	if err != nil {
		return err
	}
	again, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, again) {
		return errors.New("sharedpostgres: source changed; stop legacy writers")
	}
	if err = validHeader(header); err != nil {
		return err
	}
	lines := bytes.Split(raw[:len(raw)-1], []byte{'\n'})
	records := [][]byte{lines[0]}
	var rawHeader session.Header
	if err = json.Unmarshal(lines[0], &rawHeader); err != nil {
		return err
	}
	if rawHeader != header {
		return errors.New("sharedpostgres: source header changed")
	}
	for _, line := range lines[1:] {
		var wire struct {
			Kind  string            `json:"kind"`
			Items []json.RawMessage `json:"items"`
		}
		if err = json.Unmarshal(line, &wire); err != nil {
			return err
		}
		if wire.Kind == "batch" {
			for _, item := range wire.Items {
				records = append(records, item)
			}
		} else {
			records = append(records, line)
		}
	}
	if len(records) != len(log)+1 {
		return errors.New("sharedpostgres: source record count mismatch")
	}
	for i, item := range log {
		if err = validItem(item, uint64(i+1)); err != nil {
			return err
		}
		var original session.LogItem
		if err = json.Unmarshal(records[i+1], &original); err != nil {
			return err
		}
		// Snapshot cloning compacts RawMessage whitespace. Compare detached
		// semantic records, while still storing the untouched source bytes.
		if !reflect.DeepEqual(clone(original), clone(item)) {
			return errors.New("sharedpostgres: source records changed")
		}
	}
	return importRecords(ctx, db, domain, records)
}

func importRecords(ctx context.Context, db *sql.DB, domain string, records [][]byte) error {
	const timeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tx, err := begin(ctx, db, timeout)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO pi_superagent_v1.domains (domain,header,owner,epoch,expires,seq) VALUES ($1,$2,'',0,'epoch'::timestamptz,$3) ON CONFLICT (domain) DO NOTHING`, domain, records[0], len(records)-1)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrExists
	}
	if n != 1 {
		return errors.New("sharedpostgres: unexpected domain insert count")
	}
	for i, raw := range records[1:] {
		if _, err = tx.ExecContext(ctx, `INSERT INTO pi_superagent_v1.transitions (domain,seq,record) VALUES ($1,$2,$3)`, domain, i+1, raw); err != nil {
			return err
		}
	}
	return commit(ctx, tx)
}
