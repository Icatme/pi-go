package pigo

import (
	"fmt"
	"runtime"
	"strings"
)

func openAICodexUserAgent() string {
	if runtime.GOOS == "js" {
		return "pi (browser)"
	}

	platform := runtime.GOOS
	if platform == "windows" {
		platform = "win32"
	}

	architecture := runtime.GOARCH
	switch architecture {
	case "386":
		architecture = "ia32"
	case "amd64":
		architecture = "x64"
	}

	release := strings.TrimSpace(openAICodexOSRelease())
	if release == "" {
		release = "unknown"
	}
	return fmt.Sprintf("pi (%s %s; %s)", platform, release, architecture)
}
