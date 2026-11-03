//go:build linux && !android

package fstools

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func TestReadRejectsSpecialInodes(t *testing.T) {
	dir, _, tool := readFixture(t, ReadOptions{})
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("text"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".", "pipe", "link"} {
		if got := readCall(t.Context(), tool, p); got.Err == nil {
			t.Fatal("accepted special inode", p)
		}
	}
	// O_PATH may name a device, but the read tool must never open it for I/O.
	dev, err := os.OpenRoot("/dev")
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	deviceTool, err := ReadDefinition(dev, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := readCall(t.Context(), deviceTool, "null"); got.Err == nil {
		t.Fatal("device accepted")
	}
	proc, err := os.OpenRoot("/proc")
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Close()
	procTool, err := ReadDefinition(proc, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := readCall(t.Context(), procTool, "version"); got.Err == nil {
		t.Fatal("procfs regular-mode file accepted")
	}
}
func TestReadRejectsSocket(t *testing.T) {
	dir, _, tool := readFixture(t, ReadOptions{})
	listener, err := net.Listen("unix", filepath.Join(dir, "socket"))
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			t.Skip("execution environment prohibits creating a Unix socket fixture:", err)
		}
		t.Fatal(err)
	}
	defer listener.Close()
	if got := readCall(t.Context(), tool, "socket"); got.Err == nil {
		t.Fatal("socket inode accepted")
	}
}

func TestReadPinnedInodeSurvivesPathReplacement(t *testing.T) {
	for _, replacement := range []string{"fifo", "outside symlink", "regular", "unlink"} {
		t.Run(replacement, func(t *testing.T) {
			dir, _, tool := readFixture(t, ReadOptions{})
			file := filepath.Join(dir, "file")
			if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "secret")
			if err := os.WriteFile(outside, []byte("outside secret"), 0600); err != nil {
				t.Fatal(err)
			}
			checks := 0
			result, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: map[string]any{"path": "file"}, CheckPermission: func(context.Context) error {
				checks++
				if checks != 2 {
					return nil
				} // After O_PATH, Fstat and filesystem check; before reopen.
				if err := os.Remove(file); err != nil {
					return err
				}
				switch replacement {
				case "fifo":
					return syscall.Mkfifo(file, 0600)
				case "outside symlink":
					return os.Symlink(outside, file)
				case "regular":
					return os.WriteFile(file, []byte("replacement"), 0600)
				}
				return nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			var output readOutput
			if err := json.Unmarshal(result.StructuredContent, &output); err != nil {
				t.Fatal(err)
			}
			if output.Text != "original" {
				t.Fatalf("read replacement instead of pin: %q", output.Text)
			}
		})
	}
}
func TestReadIntermediateSymlinkAndPermissions(t *testing.T) {
	dir, _, tool := readFixture(t, ReadOptions{})
	if err := os.Mkdir(filepath.Join(dir, "inside"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "inside", "file")
	if err := os.WriteFile(file, []byte("text"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inside", filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	if got := readDecoded(t, readCall(t.Context(), tool, "alias/file")); got != "text" {
		t.Fatal(got)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "outside")); err != nil {
		t.Fatal(err)
	}
	if got := readCall(t.Context(), tool, "outside/secret"); got.Err == nil {
		t.Fatal("intermediate symlink escaped")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses filesystem read bits")
	}
	if err := os.Chmod(file, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(file, 0600)
	if got := readCall(t.Context(), tool, "inside/file"); got.Err == nil {
		t.Fatal("OS read permissions bypassed")
	}
}

func TestReadPinnedInodeSurvivesRootRenameAndClose(t *testing.T) {
	for _, action := range []string{"rename", "close"} {
		t.Run(action, func(t *testing.T) {
			dir, root, tool := readFixture(t, ReadOptions{})
			if err := os.WriteFile(filepath.Join(dir, "file"), []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			moved := dir + "-moved"
			t.Cleanup(func() { os.RemoveAll(moved) })
			checks := 0
			result, err := tool.Execute(t.Context(), agent.ToolExecutionContext{Args: map[string]any{"path": "file"}, CheckPermission: func(context.Context) error {
				checks++
				if checks != 2 {
					return nil
				}
				if action == "close" {
					return root.Close()
				}
				if err := os.Rename(dir, moved); err != nil {
					return err
				}
				if err := os.Mkdir(dir, 0700); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(dir, "file"), []byte("wrong root"), 0600)
			}})
			if err != nil {
				t.Fatal(err)
			}
			var out readOutput
			if err := json.Unmarshal(result.StructuredContent, &out); err != nil {
				t.Fatal(err)
			}
			// Closing the host's root does not revoke already-open descriptors; a host
			// must use the permission hook to revoke in-flight calls.
			if out.Text != "original" {
				t.Fatal(out)
			}
		})
	}
}
