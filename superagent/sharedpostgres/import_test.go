package sharedpostgres

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Icatme/pi-go/agent/session"
)

func legacyFile(t *testing.T, batch bool) (string, [][]byte) {
	t.Helper()
	header := []byte(`{ "kind":"session", "version":2, "id":"superagent", "created_at":1 }`)
	one := []byte(`{"kind":"entry","seq":1,"entry":{"type":"custom","id":"1","seq":1,"lane":"main","timestamp":1,"custom":{"custom_type":"superagent.transition","payload":{ "large":9007199254740993, "escaped":"\u4e2d", "a":1, "a":2 }}}}`)
	two := bytes.ReplaceAll(one, []byte(`"seq":1`), []byte(`"seq":2`))
	two = bytes.ReplaceAll(two, []byte(`"id":"1"`), []byte(`"id":"2","parent_id":"1"`))
	raw := append(append([]byte{}, header...), '\n')
	if batch {
		raw = append(raw, []byte(`{"kind":"batch","items":[`)...)
		raw = append(raw, one...)
		raw = append(raw, ',')
		raw = append(raw, two...)
		raw = append(raw, ']', '}', '\n')
	} else {
		raw = append(raw, one...)
		raw = append(raw, '\n')
		raw = append(raw, two...)
		raw = append(raw, '\n')
	}
	p := filepath.Join(t.TempDir(), "source.jsonl")
	if err := os.WriteFile(p, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return p, [][]byte{header, one, two}
}
func TestImportRetainsRawBytesAndSource(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "batch"}[batch], func(t *testing.T) {
			path, records := legacyFile(t, batch)
			before, _ := os.ReadFile(path)
			s := beginSteps()
			for i, raw := range records {
				idx := i
				want := raw
				fragment := "INSERT INTO pi_superagent_v1.transitions"
				if i == 0 {
					fragment = "ON CONFLICT"
				}
				s = append(s, step{op: "exec", sql: fragment, hook: func(_ context.Context, args []driver.NamedValue) error {
					pos := 2
					if idx == 0 {
						pos = 1
					}
					got, ok := args[pos].Value.([]byte)
					if !ok || !bytes.Equal(got, want) {
						t.Errorf("record %d JSON bytes changed: %q", idx, got)
					}
					return nil
				}})
			}
			s = append(s, step{op: "commit"})
			db, _ := scriptedDB(t, s...)
			if err := ImportJSONL(t.Context(), db, "new", path); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("source modified")
			}
		})
	}
}
func TestImportRollbackAndUnknownCommit(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "unknown commit"}[unknown], func(t *testing.T) {
			path, _ := legacyFile(t, false)
			s := beginSteps()
			s = append(s, step{op: "exec", sql: "ON CONFLICT"})
			if unknown {
				s = append(s, step{op: "exec", sql: "transitions"}, step{op: "exec", sql: "transitions"}, step{op: "commit", err: errors.New("ack lost")})
			} else {
				s = append(s, step{op: "exec", sql: "transitions", err: errors.New("insert failed")}, step{op: "rollback"})
			}
			db, _ := scriptedDB(t, s...)
			err := ImportJSONL(t.Context(), db, "new", path)
			if err == nil {
				t.Fatal("failure hidden")
			}
			if errors.Is(err, ErrCommitUnknown) != unknown {
				t.Fatalf("wrong uncertainty classification: %v", err)
			}
		})
	}
}
func TestImportRejectsOwnedOrTornSourceWithoutDB(t *testing.T) {
	for _, torn := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned", true: "torn"}[torn], func(t *testing.T) {
			path, _ := legacyFile(t, false)
			if torn {
				f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.WriteString(`{"kind":`)
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			} else {
				writer, err := session.OpenJSONLStorage(path)
				if err != nil {
					t.Fatal(err)
				}
				defer writer.Close()
			}
			before, _ := os.ReadFile(path)
			db, _ := scriptedDB(t)
			if err := ImportJSONL(t.Context(), db, "new", path); err == nil {
				t.Fatal("unsafe source accepted")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("source repaired or changed")
			}
		})
	}
}

func TestImportNeverOverwritesExistingDomain(t *testing.T) {
	path, _ := legacyFile(t, false)
	zero := int64(0)
	s := beginSteps()
	s = append(s, step{op: "exec", sql: "ON CONFLICT", affected: &zero}, step{op: "rollback"})
	db, _ := scriptedDB(t, s...)
	if err := ImportJSONL(t.Context(), db, "existing", path); !errors.Is(err, ErrExists) {
		t.Fatalf("existing domain import: %v", err)
	}
}
