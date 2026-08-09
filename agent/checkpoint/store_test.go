package checkpoint

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestMemoryStoreCompareAndSwapAndCloneOwnership(t *testing.T) {
	store := NewMemoryStore()
	id := CheckpointID("checkpoint")
	payload := []byte("one")
	record, err := store.CompareAndSwap(context.Background(), id, 0, payload)
	if err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}
	if record.Revision != 1 || string(record.Payload) != "one" {
		t.Fatalf("unexpected created record: %+v", record)
	}
	payload[0] = 'X'
	record.Payload[0] = 'Y'

	loaded, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if loaded.Revision != 1 || string(loaded.Payload) != "one" {
		t.Fatalf("caller mutation leaked into store: %+v", loaded)
	}
	loaded.Payload[0] = 'Z'
	loadedAgain, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("load checkpoint again: %v", err)
	}
	if string(loadedAgain.Payload) != "one" {
		t.Fatalf("loaded payload aliases store: %q", loadedAgain.Payload)
	}

	if _, err := store.CompareAndSwap(context.Background(), id, 0, []byte("bad")); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("expected create conflict, got %v", err)
	}
	updated, err := store.CompareAndSwap(context.Background(), id, 1, []byte("two"))
	if err != nil {
		t.Fatalf("update checkpoint: %v", err)
	}
	if updated.Revision != 2 || string(updated.Payload) != "two" {
		t.Fatalf("unexpected updated record: %+v", updated)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Load(canceled, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled load, got %v", err)
	}
}

func TestMemoryStoreConcurrentCASHasOneWinner(t *testing.T) {
	store := NewMemoryStore()
	id := CheckpointID("concurrent")
	if _, err := store.CompareAndSwap(context.Background(), id, 0, []byte("initial")); err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}

	const contenders = 32
	var winners atomic.Int64
	var conflicts atomic.Int64
	var wait sync.WaitGroup
	start := make(chan struct{})
	for index := range contenders {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, err := store.CompareAndSwap(context.Background(), id, 1, []byte{byte(index)})
			switch {
			case err == nil:
				winners.Add(1)
			case errors.Is(err, ErrRevisionConflict):
				conflicts.Add(1)
			default:
				t.Errorf("unexpected CAS error: %v", err)
			}
		}(index)
	}
	close(start)
	wait.Wait()
	if winners.Load() != 1 || conflicts.Load() != contenders-1 {
		t.Fatalf("winners=%d conflicts=%d", winners.Load(), conflicts.Load())
	}
	loaded, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("load winner: %v", err)
	}
	if loaded.Revision != 2 {
		t.Fatalf("unexpected revision %d", loaded.Revision)
	}
}
