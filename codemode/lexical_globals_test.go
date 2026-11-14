package codemode

import (
	"context"
	"encoding/json"
	"testing"
)

// Upstream pi #11226: injected function parameters must not reserve these names.
func TestScriptMayShadowDiscoveryGlobalsLocally(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	calls := 0
	tool := Tool{Name: "read", Description: "Read fixture", Invoke: func(context.Context, HostCall) (json.RawMessage, error) { calls++; return json.RawMessage(`true`), nil }}
	options := RunOptions{Tools: []Tool{tool}}
	for _, script := range []string{
		`const tools = await searchTools("read"); return tools.length;`,
		`const console = {log: value => value}; return console.log(1);`,
	} {
		result, err := s.Run(t.Context(), script, options)
		if err != nil || textOutput(result) != "1" || len(result.Calls) != 0 {
			t.Fatalf("shadowing failed: %+v %v", result, err)
		}
	}
	result, err := s.Run(t.Context(), `console.log(await tools.read({}));`, options)
	if err != nil || textOutput(result) != "true" || calls != 1 || len(result.Calls) != 1 {
		t.Fatalf("shadow escaped script scope: %+v %v calls=%d", result, err, calls)
	}
}
