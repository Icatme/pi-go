//go:build windows

package pigo

import (
	"fmt"
	"syscall"
	"unsafe"
)

type rtlOSVersionInfo struct {
	size        uint32
	major       uint32
	minor       uint32
	build       uint32
	platformID  uint32
	servicePack [128]uint16
}

func openAICodexOSRelease() string {
	version := rtlOSVersionInfo{}
	version.size = uint32(unsafe.Sizeof(version))
	status, _, _ := syscall.NewLazyDLL("ntdll.dll").NewProc("RtlGetVersion").Call(uintptr(unsafe.Pointer(&version)))
	if status != 0 {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", version.major, version.minor, version.build)
}
