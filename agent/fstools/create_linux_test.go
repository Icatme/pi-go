//go:build linux && !android

package fstools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"golang.org/x/sys/unix"
)

func TestCreateRefusesEveryExistingName(t *testing.T) {
	dir, _, tool := createFixture(t, CreateOptions{})
	outside := t.TempDir()
	target := filepath.Join(outside, "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "missing"), filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, filepath.Join(dir, "hardlink")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"link", "dangling", "fifo", "directory", "hardlink"} {
		_, err := executeCaptured(t, t.Context(), tool.Execute, agent.ToolExecutionContext{Args: createArgs(name, "new")})
		if err == nil {
			t.Fatalf("existing %s accepted", name)
		}
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "unchanged" {
		t.Fatalf("target altered %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("followed dangling link")
	}
}
func TestCreateParentSymlinkAndReplacement(t *testing.T) {
	dir, _, tool := createFixture(t, CreateOptions{})
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "inside"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inside", filepath.Join(dir, "safe")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := executeCaptured(t, t.Context(), tool.Execute, agent.ToolExecutionContext{Args: createArgs("safe/created", "complete")}); err != nil {
		t.Fatal(err)
	}
	if _, err := executeCaptured(t, t.Context(), tool.Execute, agent.ToolExecutionContext{Args: createArgs("escape/created", "escape")}); err == nil {
		t.Fatal("escaped through parent")
	}
	checks := 0
	_, err := executeCaptured(t, t.Context(), tool.Execute, agent.ToolExecutionContext{Args: createArgs("inside/pinned", "complete"), CheckPermission: func(context.Context) error {
		checks++
		if checks == 4 {
			if err := os.Rename(filepath.Join(dir, "inside"), filepath.Join(dir, "moved")); err != nil {
				return err
			}
			return os.Symlink(outside, filepath.Join(dir, "inside"))
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if checks < 4 {
		t.Fatal("replacement stage not reached")
	}
	data, err := os.ReadFile(filepath.Join(dir, "moved", "pinned"))
	if err != nil || string(data) != "complete" {
		t.Fatalf("lost pinned parent %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "pinned")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("followed replacement parent")
	}
}
func TestCreateRevocationLeavesNoNamedStagingFiles(t *testing.T) {
	dir, _, tool := createFixture(t, CreateOptions{})
	checks := 0
	_, err := executeCaptured(t, t.Context(), tool.Execute, agent.ToolExecutionContext{Args: createArgs("new", "complete"), CheckPermission: func(context.Context) error {
		checks++
		if checks == 4 {
			return errors.New("revoked")
		}
		return nil
	}})
	if err == nil || checks != 4 {
		t.Fatalf("%d %v", checks, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("named residue %v %v", entries, err)
	}
}
