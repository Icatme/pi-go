package codemodetool

import (
	"strings"
	"testing"
)

func TestDiscoveryGuidanceMarksAsyncHelpers(t *testing.T) {
	s := newSandbox(t)
	tool, e := New(s, nil, Options{})
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"ALL_TOOLS, await searchTools(", "await describeTool(name)", "await describeNamespace(name)"} {
		if !strings.Contains(tool.Description, want) {
			t.Fatalf("missing %q: %s", want, tool.Description)
		}
	}
	const custom = "Host-owned description"
	other, e := New(s, nil, Options{Description: custom})
	if e != nil || other.Description != custom {
		t.Fatal(other.Description, e)
	}
}
