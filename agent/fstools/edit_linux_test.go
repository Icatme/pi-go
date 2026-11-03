//go:build linux && !android

package fstools

import (
	"github.com/Icatme/pi-go/agent"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestEditRejectsSpecialSourcesAndRetainsOldHardLink(t *testing.T) {
	dir, _, tool := editFixture(t, "old", EditOptions{})
	if err := os.Link(filepath.Join(dir, "file"), filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"link", "fifo"} {
		if _, err := executeCaptured(t, t.Context(), tool.Execute, agent.ToolExecutionContext{Args: previewArgs(path, "old", "new")}); err == nil {
			t.Fatal("accepted special source")
		}
	}
	if err := os.Chmod(filepath.Join(dir, "file"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := executeCaptured(t, t.Context(), tool.Execute, agent.ToolExecutionContext{Args: previewArgs("file", "old", "new")}); err != nil {
		t.Fatal(err)
	}
	old, _ := os.ReadFile(filepath.Join(dir, "alias"))
	got, _ := os.ReadFile(filepath.Join(dir, "file"))
	st, _ := os.Stat(filepath.Join(dir, "file"))
	if string(old) != "old" || string(got) != "new" || st.Mode().Perm()&^0600 != 0 {
		t.Fatal("inode or metadata contract")
	}
}
