package pigo

import "testing"

func TestRegistryReplaceSwapsLazyEntryWithoutResolvingFactory(t *testing.T) {
	registry := NewRegistry[string, int]()
	factoryCalls := 0
	registry.Register("value", nil, func() int {
		factoryCalls++
		return 1
	})

	replacement := 2
	if !registry.Replace("value", &replacement) {
		t.Fatal("expected registered entry to be replaced")
	}
	resolved := registry.Resolve("value")
	if resolved == nil || *resolved != 2 || factoryCalls != 0 {
		t.Fatalf("unexpected replacement result: value=%v factoryCalls=%d", resolved, factoryCalls)
	}
	if registry.Replace("missing", &replacement) {
		t.Fatal("expected missing entry replacement to fail")
	}
	if registry.Replace("value", nil) {
		t.Fatal("expected nil replacement to fail")
	}
}
