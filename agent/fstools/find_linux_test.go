//go:build linux && !android

package fstools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func TestFindDoesNotDescendChildSymlinksOrSpecialFiles(t *testing.T) {
	dir, _, tool := findFixture(t, FindOptions{})
	findDir(t, filepath.Join(dir, "inside"))
	findFile(t, filepath.Join(dir, "inside", "one.go"))
	outside := t.TempDir()
	findFile(t, filepath.Join(outside, "secret.go"))
	for name, target := range map[string]string{"alias": "inside", "outside": outside, "loop": "."} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	out := findDecoded(t, findCall(t.Context(), tool, ".", "*"))
	if out.Truncated || len(out.Entries) != 6 {
		t.Fatal(out)
	}
	kinds := map[string]string{}
	for _, e := range out.Entries {
		kinds[e.Path] = e.Type
	}
	if kinds["alias"] != "symlink" || kinds["outside"] != "symlink" || kinds["loop"] != "symlink" || kinds["pipe"] != "other" || kinds["inside/one.go"] != "file" {
		t.Fatal(kinds)
	}
	out = findDecoded(t, findCall(t.Context(), tool, "alias", "*.go"))
	if len(out.Entries) != 1 || out.Entries[0].Path != "alias/one.go" {
		t.Fatal(out)
	}
	if got := findCall(t.Context(), tool, "outside", "*"); got.Err == nil {
		t.Fatal("starting symlink escaped root")
	}
}
func TestFindDirectoryReplacementDoesNotFollowSymlinkOrFIFO(t *testing.T) {
	for _, replacement := range []string{"symlink", "fifo", "removed"} {
		t.Run(replacement, func(t *testing.T) {
			dir, _, tool := findFixture(t, FindOptions{})
			child := filepath.Join(dir, "child")
			findDir(t, child)
			findFile(t, filepath.Join(child, "original.go"))
			outside := t.TempDir()
			findFile(t, filepath.Join(outside, "secret.go"))
			checks := 0
			result, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: findArguments{Path: ".", Pattern: "*"}, CheckPermission: func(context.Context) error {
				checks++
				if checks != 3 {
					return nil
				} // Entry was read; its directory has not been opened.
				if err := os.Rename(child, filepath.Join(dir, "saved")); err != nil {
					return err
				}
				switch replacement {
				case "symlink":
					return os.Symlink(outside, child)
				case "fifo":
					return syscall.Mkfifo(child, 0600)
				}
				return nil
			}})
			if err == nil || len(result.Content) != 0 {
				t.Fatalf("replacement was followed: %+v %v", result, err)
			}
		})
	}
}
func TestFindOpenDirectoryIsStableAcrossRename(t *testing.T) {
	dir, _, tool := findFixture(t, FindOptions{})
	child := filepath.Join(dir, "child")
	findDir(t, child)
	findFile(t, filepath.Join(child, "original.go"))
	moved := filepath.Join(t.TempDir(), "moved")
	checks := 0
	result, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: findArguments{Path: ".", Pattern: "*.go"}, CheckPermission: func(context.Context) error {
		checks++
		if checks != 4 {
			return nil
		} // Child descriptor is open, before its first read.
		if err := os.Rename(child, moved); err != nil {
			return err
		}
		// Leave the original parent empty: results retain the traversed logical path.
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	var out findOutput
	if err := json.Unmarshal(result.StructuredContent, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != 1 || out.Entries[0].Path != "child/original.go" {
		t.Fatal(out)
	}
}
func TestFindClosesDirectoryDescriptors(t *testing.T) {
	dir, _, tool := findFixture(t, FindOptions{MaxDepth: 3})
	findDir(t, filepath.Join(dir, "a", "b", "c", "d"))
	before, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for range 30 {
		if got := findCall(t.Context(), tool, ".", "*"); got.Err != nil {
			t.Fatal(got.Err)
		}
	}
	after, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) > len(before) {
		t.Fatalf("directory fd leak: before %d after %d", len(before), len(after))
	}
	// Permission failure inside a child also unwinds every descriptor.
	for range 30 {
		checks := 0
		_, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: findArguments{Path: ".", Pattern: "*"}, CheckPermission: func(context.Context) error {
			checks++
			if checks == 6 {
				return context.Canceled
			}
			return nil
		}})
		if err == nil {
			t.Fatal("expected canceled child traversal")
		}
	}
	after, err = os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) > len(before) {
		t.Fatalf("error-path fd leak: before %d after %d", len(before), len(after))
	}
}
