package pigo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

const (
	commandCodeAPIKeyEnvReference       = "COMMANDCODE_API_KEY"
	commandCodeAPIKeyDollarEnvReference = "$COMMANDCODE_API_KEY"
)

// ResolveCommandCodeAPIKey follows pi-commandcode-provider v0.4.3's
// credential lookup after considering caller-supplied runtime auth. Explicit
// stream API keys are handled before this function by the transport.
func ResolveCommandCodeAPIKey(auth map[Provider]AuthConfig) string {
	if apiKey := usableCommandCodeAPIKey(ResolveAPIKey("commandcode", auth)); apiKey != "" {
		return apiKey
	}
	if apiKey := usableCommandCodeAPIKey(GetEnvAPIKey("commandcode")); apiKey != "" {
		return apiKey
	}

	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return readCommandCodeAPIKey(commandCodeAuthPaths(home))
}

func usableCommandCodeAPIKey(value string) string {
	value = strings.TrimSpace(value)
	if value == commandCodeAPIKeyEnvReference || value == commandCodeAPIKeyDollarEnvReference {
		return ""
	}
	return value
}

func commandCodeAuthPaths(home string) []string {
	return []string{
		filepath.Join(home, ".commandcode", "auth.json"),
		filepath.Join(home, ".omp", "agent", "auth.json"),
		filepath.Join(home, ".pi", "agent", "auth.json"),
	}
}

func readCommandCodeAPIKey(paths []string) string {
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var credentials map[string]any
		if err := json.Unmarshal(body, &credentials); err != nil || credentials == nil {
			continue
		}
		if apiKey := usableCommandCodeAPIKey(anyString(credentials["apiKey"])); apiKey != "" {
			return apiKey
		}
		if apiKey := usableCommandCodeAPIKey(anyString(credentials["commandcode"])); apiKey != "" {
			return apiKey
		}
		if apiKey := commandCodeCredentialAPIKey(credentials["commandcode"]); apiKey != "" {
			return apiKey
		}
		if apiKey := commandCodeCredentialAPIKey(credentials["command-code"]); apiKey != "" {
			return apiKey
		}
	}
	return ""
}

func commandCodeCredentialAPIKey(value any) string {
	record, ok := value.(map[string]any)
	if !ok {
		return ""
	}

	switch anyString(record["type"]) {
	case "api":
		return usableCommandCodeAPIKey(anyString(record["key"]))
	case "oauth":
		return usableCommandCodeAPIKey(anyString(record["access"]))
	default:
		if apiKey := usableCommandCodeAPIKey(anyString(record["key"])); apiKey != "" {
			return apiKey
		}
		return usableCommandCodeAPIKey(anyString(record["access"]))
	}
}
