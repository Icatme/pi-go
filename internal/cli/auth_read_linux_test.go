//go:build linux

package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestAudit11421AuthFIFO(t *testing.T) {
	if path := os.Getenv("PIGO_AUDIT_FIFO"); path != "" {
		loadAuth(path)
		return
	}
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAudit11421AuthFIFO$")
	cmd.Env = append(os.Environ(), "PIGO_AUDIT_FIFO="+path, "GORACE=atexit_sleep_ms=0")
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatal("loadAuth blocked on FIFO until process deadline")
	}
	if err != nil {
		t.Fatalf("auth reader subprocess failed: %v", err)
	}
}
func TestAudit11421AuthSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	path := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(target, []byte(`{"fixture":{"type":"oauth","access":"synthetic"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if len(loadAuth(path)) != 0 {
		t.Fatal("loadAuth followed project-controlled auth symlink")
	}
}
