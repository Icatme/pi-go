//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package pigo

func openAICodexOSRelease() string {
	return ""
}
