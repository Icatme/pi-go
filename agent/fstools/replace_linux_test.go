//go:build linux && !android

package fstools

import (
	"context"
	"errors"
	"github.com/Icatme/pi-go/agent"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteDenialAfterStagingReportsNotPublished(t *testing.T) {
	for _, operation := range []string{"replace", "edit"} {
		t.Run(operation, func(t *testing.T) {
			dir, root, tool := replaceFixture(t, ReplaceOptions{})
			if err := os.WriteFile(filepath.Join(dir, "target"), []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			args := createArgs("target", "new")
			if operation == "edit" {
				var err error
				tool, err = EditDefinition(root, EditOptions{})
				if err != nil {
					t.Fatal(err)
				}
				args = previewArgs("target", "old", "new")
			}
			denyAfterStaging := func(context.Context) error {
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".pigo-replace-") {
						return errors.New("permission revoked before rename")
					}
				}
				return nil
			}
			out, err := executeCaptured(t, t.Context(), tool.Execute, agent.ToolExecutionContext{Args: args, CheckPermission: denyAfterStaging})
			var failure *agent.ToolExecutionError
			if !errors.As(err, &failure) || failure.Code != agent.ToolFailurePolicyDenied || failure.Reason != operation+"_not_published" || out.committed() {
				t.Fatalf("unexpected publication facts: error=%+v committed=%v", failure, out.committed())
			}
			content, err := os.ReadFile(filepath.Join(dir, "target"))
			if err != nil || string(content) != "old" {
				t.Fatalf("target after denial: %q, %v", content, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("staging cleanup: %v, %v", entries, err)
			}
		})
	}
}

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
			err = p.publishStaged(stage, func() {})
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
	// A racing directory makes rename itself fail after the last check. Keep
	// the conservative unknown outcome once a destination syscall was issued.
	p, err = newPendingReplace(root, "raced-dir", func() error { return os.Mkdir(filepath.Join(dir, "raced-dir"), 0700) })
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := publishFileCaptured(t, t.Context(), agent.ToolExecutionContext{}, p, []byte("new"), "replace")
	var failure *agent.ToolExecutionError
	if !errors.As(err, &failure) || failure.Reason != "replace_publication_unknown" || outcome.committed() {
		t.Fatalf("unexpected rename failure: %+v, committed=%v", failure, outcome.committed())
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
		if err := p.publishStaged(name, func() { t.Fatal("attempted invalid destination") }); err == nil {
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
