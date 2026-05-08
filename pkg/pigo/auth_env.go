package pigo

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	dotEnvValues map[string]string
	dotEnvOnce   sync.Once
)

const localSupportDirName = ".pigo"

func osLookupEnv(name string) (string, bool) {
	return os.LookupEnv(name)
}

func lookupEnvValue(name string) string {
	if value, ok := osLookupEnv(name); ok && value != "" {
		return value
	}

	dotEnvOnce.Do(func() {
		dotEnvValues = loadDotEnvFile(resolveLocalSupportFilePath(".env"))
	})

	return dotEnvValues[name]
}

func loadDotEnvFile(path string) map[string]string {
	if strings.TrimSpace(path) == "" {
		return map[string]string{}
	}

	file, err := os.Open(path)
	if err != nil {
		return map[string]string{}
	}
	defer file.Close()

	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return values
}

func resolveSupportFilePath(name string) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}

	candidate := name
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}

	workingDir, err := os.Getwd()
	if err != nil {
		return name
	}

	dir := workingDir
	for {
		candidate = filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}

		parent := filepath.Dir(dir)
		if parent == dir || parent == "" {
			break
		}
		dir = parent
	}

	return name
}

func resolveLocalSupportFilePath(name string) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}

	candidate := filepath.Join(localSupportDirName, name)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}

	workingDir, err := os.Getwd()
	if err != nil {
		return candidate
	}

	dir := workingDir
	for {
		candidate = filepath.Join(dir, localSupportDirName, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}

		parent := filepath.Dir(dir)
		if parent == dir || parent == "" {
			break
		}
		dir = parent
	}

	return filepath.Join(localSupportDirName, name)
}
