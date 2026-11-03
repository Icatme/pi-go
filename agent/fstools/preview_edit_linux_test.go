//go:build linux && !android

package fstools

import (
	"github.com/Icatme/pi-go/agent"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestPreviewUsesSharedRegularFileBoundary(t *testing.T) {
	dir, _, tool := previewFixture(t, "old", PreviewEditOptions{})
	if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"fifo", "link", "."} {
		r, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: previewArgs(p, "old", "new")})
		if err == nil || len(r.Content) > 0 {
			t.Fatalf("unsafe target %s %+v %v", p, r, err)
		}
	}
}
