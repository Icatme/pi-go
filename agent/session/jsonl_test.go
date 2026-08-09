package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func TestJSONLWritesAreVisibleAndReopenableBeforeClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	storage, err := CreateJSONLStorage(path, Header{ID: "jsonl-visible", ParentSessionID: "parent-1"})
	if err != nil {
		t.Fatalf("CreateJSONLStorage: %v", err)
	}
	entry := appendTextEntry(t, storage, MainLane, "message-1", "persisted")

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile before Close: %v", err)
	}
	if !bytes.HasSuffix(content, []byte{'\n'}) {
		t.Fatal("synced JSONL content is not newline terminated")
	}
	if lines := bytes.Split(bytes.TrimSuffix(content, []byte{'\n'}), []byte{'\n'}); len(lines) != 2 {
		t.Fatalf("got %d physical records, want header plus entry", len(lines))
	}
	header, err := ReadJSONLHeader(path)
	if err != nil {
		t.Fatalf("ReadJSONLHeader while writer open: %v", err)
	}
	if header.ID != "jsonl-visible" || header.ParentSessionID != "parent-1" || header.Version != CurrentFormatVersion {
		t.Fatalf("unexpected header: %+v", header)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := OpenJSONLStorage(path)
	if err != nil {
		t.Fatalf("OpenJSONLStorage: %v", err)
	}
	defer reopened.Close()
	loaded, ok := reopened.Entry(entry.ID)
	if !ok || loaded.Message == nil || loaded.Message.Parts[0].Text != "persisted" {
		t.Fatalf("unexpected loaded entry: %+v, found=%v", loaded, ok)
	}
	second := appendTextEntry(t, reopened, MainLane, "message-2", "after reopen")
	if second.Seq != 2 || second.ParentID != entry.ID {
		t.Fatalf("append after reopen did not continue log: %+v", second)
	}
}

func TestOpenJSONLRepairsOnlyUnterminatedSyntaxTail(t *testing.T) {
	t.Run("syntax fragment", func(t *testing.T) {
		path, validPrefix := createJSONLPrefix(t)
		appendBytes(t, path, []byte(`{"kind":"entry"`))

		storage, err := OpenJSONLStorage(path)
		if err != nil {
			t.Fatalf("OpenJSONLStorage: %v", err)
		}
		if len(storage.Entries()) != 1 {
			t.Fatalf("got %d entries after tail repair, want 1", len(storage.Entries()))
		}
		if err := storage.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		repaired, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if !bytes.Equal(repaired, validPrefix) {
			t.Fatalf("repair changed valid prefix\ngot:  %q\nwant: %q", repaired, validPrefix)
		}
	})

	t.Run("complete illegal tail", func(t *testing.T) {
		path, validPrefix := createJSONLPrefix(t)
		appendBytes(t, path, []byte(`{}`))
		before, _ := os.ReadFile(path)
		if _, err := OpenJSONLStorage(path); errorCode(err) != ErrorCorruptLog {
			t.Fatalf("OpenJSONLStorage error=%v, code=%q", err, errorCode(err))
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(after, before) || bytes.Equal(after, validPrefix) {
			t.Fatal("complete illegal tail was unexpectedly truncated")
		}
	})

	t.Run("complete valid item without newline", func(t *testing.T) {
		path, _ := createJSONLPrefix(t)
		memory, err := NewMemoryStorage(Header{ID: "tail-source"})
		if err != nil {
			t.Fatalf("NewMemoryStorage: %v", err)
		}
		first := appendTextEntry(t, memory, MainLane, "message-1", "persisted")
		_ = first
		appendTextEntry(t, memory, MainLane, "message-2", "complete")
		encoded, err := json.Marshal(memory.Log()[1])
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		appendBytes(t, path, encoded)
		before, _ := os.ReadFile(path)
		if _, err := OpenJSONLStorage(path); errorCode(err) != ErrorCorruptLog {
			t.Fatalf("OpenJSONLStorage error=%v, code=%q", err, errorCode(err))
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(after, before) {
			t.Fatal("complete unterminated item was unexpectedly modified")
		}
	})

	t.Run("newline terminated syntax error", func(t *testing.T) {
		path, validPrefix := createJSONLPrefix(t)
		appendBytes(t, path, []byte("{\"kind\":\n"))
		before, _ := os.ReadFile(path)
		if _, err := OpenJSONLStorage(path); errorCode(err) != ErrorCorruptLog {
			t.Fatalf("OpenJSONLStorage error=%v, code=%q", err, errorCode(err))
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(after, before) || bytes.Equal(after, validPrefix) {
			t.Fatal("newline-terminated malformed line was unexpectedly truncated")
		}
	})
}

func TestOpenJSONLRejectsStrictLogViolations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(LogItem) LogItem
	}{
		{
			name: "sequence gap",
			mutate: func(item LogItem) LogItem {
				item.Seq++
				item.Entry.Seq++
				return item
			},
		},
		{
			name: "duplicate id",
			mutate: func(item LogItem) LogItem {
				item.Entry.ID = "message-1"
				return item
			},
		},
		{
			name: "wrong parent",
			mutate: func(item LogItem) LogItem {
				item.Entry.ParentID = "not-the-leaf"
				return item
			},
		},
		{
			name: "mismatched type and payload",
			mutate: func(item LogItem) LogItem {
				item.Entry.Type = EntryTypeCustom
				return item
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			path, _ := createJSONLPrefix(t)
			item := secondLogItem(t)
			item = testCase.mutate(item)
			appendJSONLine(t, path, item)
			if _, err := OpenJSONLStorage(path); errorCode(err) != ErrorCorruptLog {
				t.Fatalf("OpenJSONLStorage error=%v, code=%q", err, errorCode(err))
			}
		})
	}

	t.Run("unknown JSON field", func(t *testing.T) {
		path, _ := createJSONLPrefix(t)
		appendBytes(t, path, []byte("{\"kind\":\"lane\",\"seq\":2,\"lane\":{\"seq\":2,\"lane\":\"branch\",\"unknown\":true}}\n"))
		if _, err := OpenJSONLStorage(path); errorCode(err) != ErrorCorruptLog {
			t.Fatalf("OpenJSONLStorage error=%v, code=%q", err, errorCode(err))
		}
	})
}

func TestReadJSONLHeaderDoesNotOpenOrValidateBody(t *testing.T) {
	path, validPrefix := createJSONLPrefix(t)
	appendBytes(t, path, []byte(`{"torn"`))
	before, _ := os.ReadFile(path)
	header, err := ReadJSONLHeader(path)
	if err != nil {
		t.Fatalf("ReadJSONLHeader: %v", err)
	}
	if header.ID != "jsonl-base" {
		t.Fatalf("unexpected header: %+v", header)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, before) || bytes.Equal(after, validPrefix) {
		t.Fatal("ReadJSONLHeader modified or validated the body")
	}
}

func TestJSONLRejectsMalformedOrUnterminatedHeader(t *testing.T) {
	for _, content := range [][]byte{
		[]byte(`{"kind":"session","version":1,"id":"missing-newline","created_at":1}`),
		[]byte("{\"kind\":\"session\",\"version\":2,\"id\":\"wrong-version\",\"created_at\":1}\n"),
		[]byte("{\"kind\":\"session\",\"version\":1,\"id\":\"unknown-field\",\"created_at\":1,\"extra\":true}\n"),
	} {
		path := filepath.Join(t.TempDir(), "invalid.jsonl")
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		if _, err := ReadJSONLHeader(path); errorCode(err) != ErrorCorruptLog {
			t.Fatalf("ReadJSONLHeader(%q) error=%v, code=%q", content, err, errorCode(err))
		}
		if _, err := OpenJSONLStorage(path); errorCode(err) != ErrorCorruptLog {
			t.Fatalf("OpenJSONLStorage(%q) error=%v, code=%q", content, err, errorCode(err))
		}
	}
}

func TestJSONLAppendFailureDoesNotAdvanceMemoryState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	storage, err := CreateJSONLStorage(path, Header{ID: "append-failure"})
	if err != nil {
		t.Fatalf("CreateJSONLStorage: %v", err)
	}
	if err := storage.file.Close(); err != nil {
		t.Fatalf("close underlying file: %v", err)
	}
	message := agent.Message{Role: agent.RoleUser}
	if _, err := storage.AppendEntry(MainLane, NewEntry{Type: EntryTypeMessage, ID: "not-durable", Message: &message}); errorCode(err) != ErrorStorage {
		t.Fatalf("AppendEntry error=%v, code=%q", err, errorCode(err))
	}
	if len(storage.Log()) != 0 || len(storage.Entries()) != 0 || storage.Lanes()[0].LeafID != "" {
		t.Fatalf("failed append advanced in-memory state: log=%+v lanes=%+v", storage.Log(), storage.Lanes())
	}
	if _, err := storage.AppendEntry(MainLane, NewEntry{Type: EntryTypeMessage, ID: "retry", Message: &message}); errorCode(err) != ErrorStorage {
		t.Fatalf("retry after failed append error=%v, code=%q", err, errorCode(err))
	}
	firstCloseErr := storage.Close()
	if errorCode(firstCloseErr) != ErrorStorage {
		t.Fatalf("Close error=%v, code=%q", firstCloseErr, errorCode(firstCloseErr))
	}
	if secondCloseErr := storage.Close(); secondCloseErr != firstCloseErr {
		t.Fatalf("second Close error=%v, want cached %v", secondCloseErr, firstCloseErr)
	}
	reopened, err := OpenJSONLStorage(path)
	if err != nil {
		t.Fatalf("OpenJSONLStorage after failed append: %v", err)
	}
	defer reopened.Close()
	if len(reopened.Log()) != 0 {
		t.Fatalf("failed append became durable: %+v", reopened.Log())
	}
}

func TestJSONLSyncFailureRollsBackDurableRecordAndPoisonsWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	storage, err := CreateJSONLStorage(path, Header{ID: "sync-failure"})
	if err != nil {
		t.Fatalf("CreateJSONLStorage: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile before append: %v", err)
	}

	syncCalls := 0
	storage.syncFile = func() error {
		syncCalls++
		if syncCalls == 1 {
			return errors.New("injected sync failure")
		}
		return storage.file.Sync()
	}
	message := agent.Message{Role: agent.RoleUser}
	if _, err := storage.AppendEntry(MainLane, NewEntry{Type: EntryTypeMessage, ID: "not-durable", Message: &message}); errorCode(err) != ErrorStorage {
		t.Fatalf("AppendEntry error=%v, code=%q", err, errorCode(err))
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after append: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("sync failure left a durable record:\nbefore=%q\nafter=%q", before, after)
	}
	if len(storage.Log()) != 0 || len(storage.Entries()) != 0 {
		t.Fatalf("sync failure advanced memory state: log=%+v entries=%+v", storage.Log(), storage.Entries())
	}
	if _, err := storage.AppendEntry(MainLane, NewEntry{Type: EntryTypeMessage, ID: "retry", Message: &message}); errorCode(err) != ErrorStorage {
		t.Fatalf("retry after sync failure error=%v, code=%q", err, errorCode(err))
	}
	firstCloseErr := storage.Close()
	if errorCode(firstCloseErr) != ErrorStorage {
		t.Fatalf("Close error=%v, code=%q", firstCloseErr, errorCode(firstCloseErr))
	}
	if secondCloseErr := storage.Close(); secondCloseErr != firstCloseErr {
		t.Fatalf("second Close error=%v, want cached %v", secondCloseErr, firstCloseErr)
	}

	reopened, err := OpenJSONLStorage(path)
	if err != nil {
		t.Fatalf("OpenJSONLStorage after rollback: %v", err)
	}
	defer reopened.Close()
	if len(reopened.Log()) != 0 {
		t.Fatalf("rolled-back append reappeared: %+v", reopened.Log())
	}
}

func createJSONLPrefix(t *testing.T) (string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	storage, err := CreateJSONLStorage(path, Header{ID: "jsonl-base"})
	if err != nil {
		t.Fatalf("CreateJSONLStorage: %v", err)
	}
	appendTextEntry(t, storage, MainLane, "message-1", "persisted")
	if err := storage.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return path, content
}

func secondLogItem(t *testing.T) LogItem {
	t.Helper()
	storage, err := NewMemoryStorage(Header{ID: "log-source"})
	if err != nil {
		t.Fatalf("NewMemoryStorage: %v", err)
	}
	appendTextEntry(t, storage, MainLane, "message-1", "persisted")
	appendTextEntry(t, storage, MainLane, "message-2", "second")
	return storage.Log()[1]
}

func appendJSONLine(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	appendBytes(t, path, append(encoded, '\n'))
}

func appendBytes(t *testing.T, path string, content []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		t.Fatalf("Write: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
