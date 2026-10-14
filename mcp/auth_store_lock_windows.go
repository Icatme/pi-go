package mcp

import (
	"errors"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func authCreateLockDirectory(path string) (bool, error) {
	// The store provides a canonical absolute DOS path. Native NT paths use
	// \??\ for drive letters and \??\UNC\ for network shares, including the
	// extended-length DOS forms accepted by NewFileCredentialStore.
	ntPath := path
	switch {
	case strings.HasPrefix(path, `\\?\`), strings.HasPrefix(path, `\\.\`):
		ntPath = `\??\` + path[4:]
	case strings.HasPrefix(path, `\\`):
		ntPath = `\??\UNC\` + path[2:]
	default:
		ntPath = `\??\` + path
	}
	name, err := windows.NewNTUnicodeString(ntPath)
	if err != nil {
		return false, &os.PathError{Op: "mkdir", Path: path, Err: err}
	}
	attributes := windows.OBJECT_ATTRIBUTES{ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	// FILE_CREATE is exclusive. Unlike Win32 CreateDirectory, NtCreateFile
	// preserves STATUS_DELETE_PENDING instead of mapping it to ACCESS_DENIED.
	err = windows.NtCreateFile(&handle, windows.SYNCHRONIZE, &attributes, &status, nil, windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_CREATE,
		windows.FILE_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) || errors.Is(err, windows.STATUS_DELETE_PENDING) {
		return false, nil
	}
	if err != nil {
		if native, ok := err.(windows.NTStatus); ok {
			err = native.Errno()
		}
		return false, &os.PathError{Op: "mkdir", Path: path, Err: err}
	}
	// No handle is retained across lock ownership or removal.
	if err := windows.CloseHandle(handle); err != nil {
		return false, &os.PathError{Op: "close", Path: path, Err: err}
	}
	return true, nil
}
