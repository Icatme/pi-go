package session

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStrictOpenNeverRepairsAndRetainsWriterLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "strict.jsonl")
	store, err := CreateJSONLStorage(path, Header{ID: "strict"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = OpenJSONLStorageStrict(path); !errors.Is(err, &Error{Code: ErrorBusy}) {
		t.Fatalf("strict open did not respect writer: %v", err)
	}
	store.Close()
	strict, err := OpenJSONLStorageStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = OpenJSONLStorage(path); !errors.Is(err, &Error{Code: ErrorBusy}) {
		t.Fatalf("strict writer did not retain lock: %v", err)
	}
	strict.Close()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString(`{"kind":`)
	file.Close()
	before, _ := os.ReadFile(path)
	if _, err = OpenJSONLStorageStrict(path); !errors.Is(err, &Error{Code: ErrorCorruptLog}) {
		t.Fatalf("strict torn-tail: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("strict open mutated corrupt registry")
	}
}
