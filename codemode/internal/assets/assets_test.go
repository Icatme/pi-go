package assets

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func TestPinnedArtifactAndLicenseSources(t *testing.T) {
	hash := sha256.Sum256(WASM)
	if len(WASM) != 637405 || hex.EncodeToString(hash[:]) != SHA256 {
		t.Fatal("guest artifact changed without reviewed digest")
	}
	for file, want := range map[string]string{
		"interface.c.txt":      "68c6ed946041cab734fbd3d7ce68c6ba5cd79b944729229ef2237227fd333e7a",
		"LICENSE.quickjs-wasi": "f7735da3b2531ddf451937117343cfadb853fd8054b499e7a70695dc57ffd184",
		"LICENSE.quickjs-ng":   "96f73f9d2a16c21a36b418f06073be26e7d6d5e7c1bc99756b21a4f2c74ef171",
	} {
		content, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(content)
		if hex.EncodeToString(digest[:]) != want {
			t.Fatalf("source digest differs for %s", file)
		}
		if strings.HasPrefix(file, "LICENSE") && !strings.Contains(string(content), "Permission is hereby granted") {
			t.Fatalf("missing license notice %s", file)
		}
	}
}
