//go:build linux && !android

package fstools

import (
	"context"
	"github.com/Icatme/pi-go/agent"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrepSharedSafeOpen(t *testing.T) {
	dir, _, tool := grepFixture(t, "original", GrepOptions{})
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"inside": "file", "outside": filepath.Join(outside, "secret")} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"inside", "outside", "fifo", "."} {
		r, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: map[string]any{"path": name, "pattern": ".*"}})
		if err == nil || len(r.Content) > 0 {
			t.Fatalf("unsafe target %s %+v %v", name, r, err)
		}
	}
	checks := 0
	result, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: map[string]any{"path": "file", "pattern": "original"}, CheckPermission: func(context.Context) error {
		checks++
		if checks == 3 {
			if err := os.Rename(filepath.Join(dir, "file"), filepath.Join(dir, "old")); err != nil {
				return err
			}
			return unix.Mkfifo(filepath.Join(dir, "file"), 0600)
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Execute checks permission before compilation; readTextFile before pin;
	// check 3 is after pin and before reopening it. Replacement is not opened.
	if len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "original") {
		t.Fatalf("lost pinned original: %+v", result)
	}
}
