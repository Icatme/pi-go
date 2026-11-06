package sharedsqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/Icatme/pi-go/agent/session"
)

// ImportJSONL copies a quiesced legacy journal into an absent destination
// domain. Stop all old readers/writers before calling and do not restart them
// after migration. The original file is retained unchanged; the header and each LogItem
// are stored byte-for-byte (excluding newline separators and batch wrappers).
func ImportJSONL(ctx context.Context, dbPath, domain, sourcePath string) error {
	if strings.TrimSpace(domain) == "" {
		return errors.New("sharedsqlite: domain is required")
	}
	// Snapshot validation is read-only and holds the legacy writer lock.
	// The caller must keep legacy writers stopped throughout cutover.
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		return errors.New("sharedsqlite: import source must contain complete newline-terminated records")
	}
	header, log, err := session.ReadJSONLSnapshot(sourcePath)
	if err != nil {
		return err
	}
	lockedRaw, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, lockedRaw) {
		return errors.New("sharedsqlite: import source changed; quiesce legacy writers")
	}
	if err = validHeader(header); err != nil {
		return err
	}
	for i, item := range log {
		if err = validItem(item, uint64(i+1)); err != nil {
			return err
		}
	}
	lines := bytes.Split(raw[:len(raw)-1], []byte{'\n'})
	records := [][]byte{lines[0]}
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
		return errors.New("sharedsqlite: source record count mismatch")
	}
	db, err := connect(ctx, dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	return importRecords(ctx, db, domain, records)
}

func importRecords(ctx context.Context, db *sql.DB, domain string, lines [][]byte) error {
	tx, err := begin(ctx, db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM domains WHERE domain=?", domain).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrExists
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO domains VALUES(?,?, '',0,0,?)", domain, lines[0], len(lines)-1); err != nil {
		return err
	}
	for i, line := range lines[1:] {
		if _, err = tx.ExecContext(ctx, "INSERT INTO transitions VALUES(?,?,?)", domain, i+1, line); err != nil {
			return err
		}
	}
	return tx.Commit()
}
