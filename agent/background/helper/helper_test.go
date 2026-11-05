//go:build linux

package helper

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/background"
)

func request(id string) background.Request {
	return background.Request{ID: id, Scope: "fixture", Call: agent.ToolCall{ID: "call", Name: "work", Arguments: json.RawMessage(`{}`)}}
}
func config(t *testing.T) Options {
	t.Helper()
	return Options{Scope: "fixture", PolicyRevision: "v1", MaxConcurrent: 1, Helpers: []Command{{Tool: "work", Executable: os.Args[0], Args: []string{"-test.run=^TestNativeHelper$", "--"}}}, Authorize: func(context.Context, Authorization) (uint64, error) { return 1, nil }}
}
func TestDurableReservationNeverRelaunches(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backend")
	cfg := config(t)
	b, e := Create(dir, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e = b.Stop(t.Context(), "early"); e != nil {
		t.Fatal(e)
	}
	b.Close()
	b, e = Open(dir, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	if e = b.Start(t.Context(), request("early")); e == nil {
		t.Fatal("stopped identity launched")
	}
	o, e := b.Inspect(t.Context(), "early")
	if e != nil || o.State != "unknown" {
		t.Fatalf("tombstone claimed absence: %+v %v", o, e)
	}
	r := request("early")
	r.Call.Arguments = json.RawMessage(`{"different":true}`)
	if e = b.Start(t.Context(), r); !errors.Is(e, ErrInvalid) {
		t.Fatalf("immutable request replaced: %v", e)
	}
}
func TestMissingRegistryCannotBeRecreatedByOpen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	if _, e := Open(dir, config(t)); e == nil {
		t.Fatal("missing backend recovered")
	}
	if _, e := os.Stat(dir); !os.IsNotExist(e) {
		t.Fatal("Open mutated absent path")
	}
}
