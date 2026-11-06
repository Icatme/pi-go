//go:build (linux || darwin || windows) && (amd64 || arm64)

package sharedsqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/Icatme/pi-go/agent/session"
	"os"
	"path/filepath"
	"testing"
)

func legacy(t *testing.T) string {
	t.Helper()
	p := filepath.Join(privateDir(t), "legacy.jsonl")
	s, err := session.CreateJSONLStorage(p, session.Header{ID: "superagent"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AppendEntry("main", transition("1")); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestImportPreservesRecordsAndRejectsExisting(t *testing.T) {
	source := legacy(t)
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(privateDir(t), "import.db")
	ctx := context.Background()
	if err = ImportJSONL(ctx, p, "import", source); err != nil {
		t.Fatal(err)
	}
	j, err := Open(ctx, p, Options{Domain: "import", Owner: "reader"})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	lines := bytes.Split(bytes.TrimSuffix(original, []byte{'\n'}), []byte{'\n'})
	var header, record []byte
	if err = j.db.QueryRow("SELECT header FROM domains WHERE domain='import'").Scan(&header); err != nil {
		t.Fatal(err)
	}
	if err = j.db.QueryRow("SELECT record FROM transitions WHERE domain='import'").Scan(&record); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(header, lines[0]) || !bytes.Equal(record, lines[1]) {
		t.Fatal("import changed JSON bytes")
	}
	after, _ := os.ReadFile(source)
	if !bytes.Equal(original, after) {
		t.Fatal("source changed")
	}
	if err = ImportJSONL(ctx, p, "import", source); !errors.Is(err, ErrExists) {
		t.Fatalf("existing import: %v", err)
	}
	empty, err := Open(ctx, p, Options{Domain: "empty", Owner: "reader"})
	if err != nil {
		t.Fatal(err)
	}
	empty.Close()
	if err = ImportJSONL(ctx, p, "empty", source); !errors.Is(err, ErrExists) {
		t.Fatalf("existing empty import: %v", err)
	}
}
func TestImportRollbackAndSourceLock(t *testing.T) {
	ctx := context.Background()
	source := legacy(t)
	p := filepath.Join(privateDir(t), "rollback.db")
	db, err := connect(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TRIGGER fail_import BEFORE INSERT ON transitions BEGIN SELECT RAISE(ABORT,'test rollback'); END`); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(source)
	lines := bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'})
	if err = importRecords(ctx, db, "d", lines); err == nil {
		t.Fatal("expected rollback")
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM domains WHERE domain='d'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("partial domain import")
	}
	if _, err = db.Exec("DROP TRIGGER fail_import"); err != nil {
		t.Fatal(err)
	}
	locked, err := session.OpenJSONLStorage(source)
	if err != nil {
		t.Fatal(err)
	}
	if err = ImportJSONL(ctx, p, "d", source); err == nil {
		t.Fatal("imported locked source")
	}
	locked.Close()
	if err = ImportJSONL(ctx, p, "d", source); err != nil {
		t.Fatal(err)
	}
}
func TestImportRejectsTornSourceWithoutRepair(t *testing.T) {
	source := legacy(t)
	f, err := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"kind":`)
	f.Close()
	before, _ := os.ReadFile(source)
	if err = ImportJSONL(context.Background(), filepath.Join(privateDir(t), "bad.db"), "d", source); err == nil {
		t.Fatal("accepted torn source")
	}
	after, _ := os.ReadFile(source)
	if !bytes.Equal(before, after) {
		t.Fatal("repaired source")
	}
}

func TestImportBatchPreservesRawItems(t *testing.T) {
	source := legacy(t)
	s, err := session.OpenJSONLStorage(source)
	if err != nil {
		t.Fatal(err)
	}
	branch, err := s.ReadBranch("main")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.CompareAppend(branch.Version, []session.NewEntry{transition("2"), transition("3")}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	raw, _ := os.ReadFile(source)
	lines := bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'})
	var batch struct {
		Items []json.RawMessage `json:"items"`
	}
	if err = json.Unmarshal(lines[2], &batch); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(privateDir(t), "batch.db")
	if err = ImportJSONL(context.Background(), p, "d", source); err != nil {
		t.Fatal(err)
	}
	j, err := Open(context.Background(), p, Options{Domain: "d", Owner: "reader"})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if len(j.Log()) != 3 {
		t.Fatal("missing imported batch")
	}
	for i, item := range batch.Items {
		var record []byte
		if err = j.db.QueryRow("SELECT record FROM transitions WHERE domain='d' AND seq=?", i+2).Scan(&record); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(record, item) {
			t.Fatal("batch item bytes changed")
		}
	}
}
