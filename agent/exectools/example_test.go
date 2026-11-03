package exectools_test

import (
	"context"
	"io"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/exectools"
)

// This compile-only example binds a host-owned execution profile. It does not
// install or run a backend. In an application, submit owns isolation, tenant
// identity, authorization, resource limits and actual job termination/reaping.
func ExampleCommandDefinition() {
	buildTool := func(submit func(context.Context, []string, io.Writer, io.Writer) (int, error)) (agent.ToolDefinition, error) {
		return exectools.CommandDefinition(submit, exectools.Options{MaxOutputBytes: 8192})
	}
	_ = buildTool
	// Pass the returned definition to an explicit receipt-aware Agent run, or
	// wrap it with codemodetool.Native. Retain the host receipt handle on errors;
	// neither this definition nor receipt collection prevents duplicate jobs.
}
