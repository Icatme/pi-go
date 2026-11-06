package session

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadJSONLSnapshotDoesNotRepairAndExcludesWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.jsonl")
	store, err := CreateJSONLStorage(path, Header{ID: "snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadJSONLSnapshot(path); !errors.Is(err, &Error{Code: ErrorBusy}) {
		t.Fatalf("read while owned: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.WriteString(`{"kind":`); err != nil {
		t.Fatal(err)
	}
	file.Close()
	before, _ := os.ReadFile(path)
	if _, _, err := ReadJSONLSnapshot(path); !errors.Is(err, &Error{Code: ErrorCorruptLog}) {
		t.Fatalf("torn tail accepted: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("snapshot read changed source")
	}
}

func TestReadJSONLSnapshotPreservesTransactions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.jsonl")
	store, err := CreateJSONLStorage(path, Header{ID: "snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := store.ReadBranch(MainLane)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CompareAppend(branch.Version, []NewEntry{{Type: EntryTypeCustom, ID: "1", Custom: &CustomData{CustomType: "fixture"}}, {Type: EntryTypeCustom, ID: "2", Custom: &CustomData{CustomType: "fixture"}}}); err != nil {
		t.Fatal(err)
	}
	wantHeader, wantLog := store.Header(), store.Log()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	header, log, err := ReadJSONLSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if header != wantHeader || !reflect.DeepEqual(log, wantLog) {
		t.Fatal("transaction snapshot changed")
	}
	log[0].Entry.ID = "detached"
	_, again, err := ReadJSONLSnapshot(path)
	if err != nil || !reflect.DeepEqual(again, wantLog) {
		t.Fatal("snapshot aliased source")
	}
}
