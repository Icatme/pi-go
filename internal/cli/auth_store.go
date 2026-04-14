package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const localDataDirName = ".pigo"
const authFileName = "auth.json"

type storedOAuthCredentials struct {
	Type      string `json:"type"`
	Access    string `json:"access"`
	Refresh   string `json:"refresh"`
	Expires   int64  `json:"expires"`
	AccountID string `json:"accountId,omitempty"`
}

func loadAuth(path string) map[string]storedOAuthCredentials {
	body, err := os.ReadFile(path)
	if err != nil {
		return map[string]storedOAuthCredentials{}
	}

	var auth map[string]storedOAuthCredentials
	if err := json.Unmarshal(body, &auth); err != nil {
		return map[string]storedOAuthCredentials{}
	}
	if auth == nil {
		return map[string]storedOAuthCredentials{}
	}
	return auth
}

func saveAuth(path string, auth map[string]storedOAuthCredentials) error {
	if auth == nil {
		auth = map[string]storedOAuthCredentials{}
	}

	body, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return err
	}
	return nil
}

func resolveAuthPath() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if wd == "" {
		return "", errors.New("failed to resolve working directory")
	}
	return filepath.Join(wd, localDataDirName, authFileName), nil
}
