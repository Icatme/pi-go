//go:build linux && !android

package fstools

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestFIFOPathDoesNotBlock(t *testing.T) {
	dir, _, tool := fixture(t, Options{})
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	// Synchronous on purpose: the test timeout catches regressions instead of
	// leaving a blocked worker behind and claiming context canceled the syscall.
	if got := call(t.Context(), tool, "pipe"); got.Err == nil {
		t.Fatal("FIFO accepted")
	}
	if err := os.Symlink("pipe", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if got := call(t.Context(), tool, "link"); got.Err == nil {
		t.Fatal("FIFO symlink accepted")
	}
}

func TestConcurrentSymlinkReplacementCannotEscape(t *testing.T) {
	dir, _, tool := fixture(t, Options{})
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside-secret"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "inside"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inside", "public"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	func() {
		done := make(chan struct{})
		stop := make(chan struct{})
		ready := make(chan struct{})
		mutationErr := make(chan error, 1)
		go func() {
			defer close(done)
			started := false
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, target := range []string{"inside", outside} {
					temp := filepath.Join(dir, "replacement")
					if err := os.Symlink(target, temp); err != nil {
						mutationErr <- err
						return
					}
					if err := os.Rename(temp, filepath.Join(dir, "flip")); err != nil {
						mutationErr <- err
						return
					}
					if !started {
						close(ready)
						started = true
					}
				}
			}
		}()
		defer func() {
			close(stop)
			<-done
			select {
			case err := <-mutationErr:
				t.Error("symlink mutation failed:", err)
			default:
			}
		}()
		select {
		case <-ready:
		case err := <-mutationErr:
			t.Fatal("symlink mutation never started:", err)
		}
		for range 200 {
			got := call(t.Context(), tool, "flip")
			if got.Err != nil {
				continue
			} // Missing or escaping target during replacement.
			out := decoded(t, got)
			for _, e := range out.Entries {
				if e.Name != "public" {
					t.Fatalf("escaped: %+v", out)
				}
			}
		}
	}()
	// Do not require success while the link is deliberately unstable, but prove
	// the same path is readable once mutation has stopped.
	if err := os.Remove(filepath.Join(dir, "flip")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inside", filepath.Join(dir, "flip")); err != nil {
		t.Fatal(err)
	}
	out := decoded(t, call(t.Context(), tool, "flip"))
	if len(out.Entries) != 1 || out.Entries[0].Name != "public" {
		t.Fatal(out)
	}

}

func TestOSDirectoryPermissionsAreNotBypassed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may bypass filesystem access bits")
	}
	dir, _, tool := fixture(t, Options{})
	child := filepath.Join(dir, "private")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(child, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(child, 0700)
	if got := call(t.Context(), tool, "private"); got.Err == nil {
		t.Fatal("unreadable directory accepted")
	}
}
