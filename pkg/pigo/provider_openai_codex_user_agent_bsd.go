//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package pigo

import "syscall"

func openAICodexOSRelease() string {
	release, err := syscall.Sysctl("kern.osrelease")
	if err != nil {
		return ""
	}
	return release
}
