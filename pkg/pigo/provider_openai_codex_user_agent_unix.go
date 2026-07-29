//go:build aix || linux

package pigo

import "syscall"

func openAICodexOSRelease() string {
	var info syscall.Utsname
	if err := syscall.Uname(&info); err != nil {
		return ""
	}

	release := make([]byte, 0, len(info.Release))
	for _, value := range info.Release {
		if value == 0 {
			break
		}
		release = append(release, byte(value))
	}
	return string(release)
}
