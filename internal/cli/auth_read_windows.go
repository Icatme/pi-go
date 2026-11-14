package cli

import (
	"golang.org/x/sys/windows"
	"os"
)

func openCLIAuthRead(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// Open the final reparse point itself, never follow a replacement symlink.
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(h), path)
	var attrs windows.ByHandleFileInformation
	kind, kindErr := windows.GetFileType(h)
	if err = windows.GetFileInformationByHandle(h, &attrs); err != nil || kindErr != nil || kind != windows.FILE_TYPE_DISK || attrs.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		file.Close()
		return nil, os.ErrInvalid
	}
	return file, nil
}
