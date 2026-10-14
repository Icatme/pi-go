package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestOAuthFileStoreDeletePendingLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store, err := NewFileCredentialStore(path)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := path + ".lock"
	if err := os.Mkdir(lockPath, 0700); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if handle != windows.InvalidHandle {
			windows.CloseHandle(handle)
		}
	})
	deletePending := byte(1)
	if err := windows.SetFileInformationByHandle(handle, windows.FileDispositionInfo, &deletePending, uint32(unsafe.Sizeof(deletePending))); err != nil {
		t.Fatal(err)
	}
	// The old writer removed its directory, but another process's open handle
	// keeps deletion pending. Win32 Mkdir reports this as access denied.
	if err := os.Mkdir(lockPath, 0700); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("expected delete-pending Win32 access denied, got %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Millisecond)
	defer cancel()
	if _, err := store.Load(ctx, authStoreTestKey()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("delete-pending lock must wait for ownership, got %v", err)
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	handle = windows.InvalidHandle
	if _, err := store.Load(t.Context(), authStoreTestKey()); err != nil {
		t.Fatalf("lock unavailable after final deletion handle closed: %v", err)
	}
}

func TestOAuthFileStoreLockPermissionDenied(t *testing.T) {
	parent := t.TempDir()
	store, err := NewFileCredentialStore(filepath.Join(parent, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	original, err := windows.GetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	originalACL, _, err := original.DACL()
	if err != nil {
		t.Fatal(err)
	}
	// Deny directory creation in this synthetic temporary parent while keeping
	// other access, including cleanup and restoring the original ACL.
	denied, err := windows.SecurityDescriptorFromString("D:(D;;0x4;;;WD)(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	deniedACL, _, err := denied.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, deniedACL, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := windows.SetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, originalACL, nil); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err = store.Load(ctx, authStoreTestKey())
	if !errors.Is(err, errOAuthStore) || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("real permission denial must fail instead of waiting: %v", err)
	}
}

func TestOAuthLockDirectoryExtendedPath(t *testing.T) {
	path := `\\?\` + filepath.Join(t.TempDir(), "lock")
	created, err := authCreateLockDirectory(path)
	if err != nil || !created {
		t.Fatalf("extended path creation: created=%v err=%v", created, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}
