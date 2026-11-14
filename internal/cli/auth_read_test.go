package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestAuthReadBoundedRegularFiles(t *testing.T) {
	const valid = `{"fixture":{"type":"oauth","access":"synthetic"}}`
	for _, size := range []int{len(valid), maxCLIAuthBytes, maxCLIAuthBytes + 1} {
		path := filepath.Join(t.TempDir(), "auth.json")
		data := append([]byte(valid), bytes.Repeat([]byte(" "), size-len(valid))...)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		got := loadAuth(path)
		if size <= maxCLIAuthBytes {
			if got["fixture"].Access != "synthetic" {
				t.Fatalf("valid auth rejected size%d", size)
			}
		} else if len(got) != 0 {
			t.Fatal("oversized auth accepted")
		}
	}
	if len(loadAuth(t.TempDir())) != 0 {
		t.Fatal("directory accepted")
	}
}
