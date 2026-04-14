package cli

import (
	"path/filepath"
	"testing"
)

func TestLoadAuthMissingFileReturnsEmptyMap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	auth := loadAuth(path)
	if len(auth) != 0 {
		t.Fatalf("expected empty auth map, got %+v", auth)
	}
}

func TestSaveAuthRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	expected := map[string]storedOAuthCredentials{
		"openai-codex": {
			Type:      "oauth",
			Access:    "access-token",
			Refresh:   "refresh-token",
			Expires:   123456789,
			AccountID: "acc_test",
		},
	}

	if err := saveAuth(path, expected); err != nil {
		t.Fatalf("saveAuth returned error: %v", err)
	}

	got := loadAuth(path)
	if len(got) != 1 {
		t.Fatalf("expected one auth entry, got %+v", got)
	}
	if got["openai-codex"] != expected["openai-codex"] {
		t.Fatalf("expected %v, got %v", expected["openai-codex"], got["openai-codex"])
	}
}
