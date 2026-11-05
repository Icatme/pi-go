//go:build sqlite3_dotlk && (linux || darwin || windows) && (amd64 || arm64)

package sharedsqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectAlternateLocking(t *testing.T) {
	_, err := Open(context.Background(), filepath.Join(privateDir(t), "state.db"), Options{Domain: "d", Owner: "owner"})
	if err == nil || !strings.Contains(err.Error(), "unsupported locking") {
		t.Fatalf("alternate locking accepted: %v", err)
	}
}
