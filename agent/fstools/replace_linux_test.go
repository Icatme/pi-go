//go:build linux && !android

package fstools

import (
	"errors"
	"github.com/Icatme/pi-go/agent"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceRejectsObservedSpecialNames(t *testing.T) {
	dir, _, tool := replaceFixture(t, ReplaceOptions{})
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"link", "dangling", "directory", "fifo"} {
		if _, err := executeCaptured(t, t.Context(), tool.Execute, agent.ToolExecutionContext{Args: createArgs(name, "new")}); err == nil {
			t.Fatal("accepted", name)
		}
	}
	got, _ := os.ReadFile(outside)
	if string(got) != "keep" {
		t.Fatal("escaped")
	}
}
func TestReplaceStagingOwnershipAndLastWriterWins(t *testing.T) {
	for _, mode := range []string{"collision", "reuse after commit", "late denial", "concurrent update"} {
		t.Run(mode, func(t *testing.T) {
			dir, root, _ := replaceFixture(t, ReplaceOptions{})
			if err := os.WriteFile(filepath.Join(dir, "target"), []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			var before func() error
			if mode == "late denial" {
				before = func() error { return errors.New("denied") }
			}
			pending, err := newPendingReplace(root, "target", before)
			if err != nil {
				t.Fatal(err)
			}
			p := pending.(*linuxPendingReplace)
			if _, err = p.Write([]byte("new")); err != nil {
				t.Fatal(err)
			}
			const stage = ".test-owned-stage"
			if mode == "collision" {
				if err := os.WriteFile(filepath.Join(dir, stage), []byte("other"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "concurrent update" {
				if err := os.WriteFile(filepath.Join(dir, "target"), []byte("intervening update"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = p.publishStaged(stage)
			if (mode == "collision" || mode == "late denial") != (err != nil) {
				t.Fatalf("%v", err)
			}
			if mode == "reuse after commit" {
				if err := os.WriteFile(filepath.Join(dir, stage), []byte("other"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(filepath.Join(dir, "target"))
			want := "new"
			if mode == "collision" || mode == "late denial" {
				want = "old"
			}
			if string(got) != want {
				t.Fatalf("%q", got)
			}
			if mode == "collision" || mode == "reuse after commit" {
				got, err := os.ReadFile(filepath.Join(dir, stage))
				if err != nil || string(got) != "other" {
					t.Fatal("deleted unowned staging name")
				}
			} else if _, err := os.Lstat(filepath.Join(dir, stage)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("staging left", err)
			}
		})
	}
}
func TestReplaceFixedParentAndTargetRace(t *testing.T) {
	dir, root, _ := replaceFixture(t, ReplaceOptions{})
	if err := os.Mkdir(filepath.Join(dir, "parent"), 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	out := filepath.Join(outside, "target")
	if err := os.WriteFile(out, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := newPendingReplace(root, "parent/target", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "parent"), filepath.Join(dir, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "parent")); err != nil {
		t.Fatal(err)
	}
	if _, err := publishFileCaptured(t, t.Context(), agent.ToolExecutionContext{}, p, []byte("inside"), "replace"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(out)
	inside, _ := os.ReadFile(filepath.Join(dir, "moved/target"))
	if string(got) != "outside" || string(inside) != "inside" {
		t.Fatal("parent escaped")
	}
	// A final symlink introduced after the last observation is replaced as a
	// directory entry, not followed. Ordinary mode is deliberately not CAS.
	p, err = newPendingReplace(root, "raced", func() error { return os.Symlink(out, filepath.Join(dir, "raced")) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publishFileCaptured(t, t.Context(), agent.ToolExecutionContext{}, p, []byte("raced content"), "replace"); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(out)
	st, _ := os.Lstat(filepath.Join(dir, "raced"))
	if string(got) != "outside" || !st.Mode().IsRegular() {
		t.Fatal("followed final link")
	}
}

func TestReplaceStagingCannotBeDestination(t *testing.T) {
	dir, root, _ := replaceFixture(t, ReplaceOptions{})
	checks := 0
	pending, err := newPendingReplace(root, "target", func() error { checks++; return errors.New("denied") })
	if err != nil {
		t.Fatal(err)
	}
	p := pending.(*linuxPendingReplace)
	for _, name := range []string{"target", "", ".", "..", "a/b"} {
		if err := p.publishStaged(name); err == nil {
			t.Fatal("accepted", name)
		}
		entries, readErr := os.ReadDir(dir)
		if readErr != nil || len(entries) != 0 || checks != 0 || p.staging != "" {
			t.Fatal("temporary destination published before authorization", entries, checks, p.staging, readErr)
		}
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("published before authorization")
	}
}
