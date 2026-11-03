package fstools_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/codemodetool"
	"github.com/Icatme/pi-go/agent/fstools"
	"github.com/Icatme/pi-go/codemode"
)

func ExampleReadOnlyDefinitions() {
	// The application supplies this trusted directory, never model input.
	root, err := os.OpenRoot("/host/configured/workspace")
	if err != nil {
		return
	}
	defer root.Close() // In a server, wait for all tool calls before closing.
	definitions, err := fstools.ReadOnlyDefinitions(root, fstools.ReadOnlyOptions{
		Read: fstools.ReadOptions{MaxBytes: 32 << 10},
		Grep: fstools.GrepOptions{MaxScanBytes: 32 << 10},
	})
	if err != nil {
		return
	} // Unsupported platforms fail closed.

	ctx := context.Background()
	sandbox, err := codemode.NewSandbox(ctx, codemode.DefaultConfig())
	if err != nil {
		return
	}
	defer sandbox.Close(ctx)
	bindings := make([]codemodetool.Binding, 0, len(definitions))
	allowed := make(map[string]bool, len(definitions)+1)
	for _, definition := range definitions {
		binding := codemodetool.Native(definition, "workspace")
		bindings = append(bindings, binding)
		allowed[definition.Name] = true
		allowed[binding.ExportedName()] = true
	}
	code, err := codemodetool.New(sandbox, bindings, codemodetool.Options{})
	if err != nil {
		return
	}
	allowed[code.Name] = true

	// Host-authored policy for this one trusted workspace. A production host can
	// additionally inspect scope, arguments and current approvals on EVERY check.
	// The outer code call being allowed does not preapprove its leaf calls.
	check := func(_ context.Context, in agent.BeforeToolCallContext) error {
		if !allowed[in.ToolCall.Name] {
			return errors.New("tool not authorized")
		}
		return nil
	}
	// Direct model exposure: use definitions here instead of []{code}. To expose
	// both routes, append code to definitions; both use the same permission hook.
	options := agent.RunToolCallOptions{Tools: []agent.ToolDefinition{code}, CheckToolPermission: check}
	// This host-authored example calls only ls. Model-authored scripts go through
	// the exact same outer and child permission checks.
	raw, _ := json.Marshal(map[string]any{"code": `const listing = await tools.` + bindings[0].ExportedName() + `({path:"."}); text(listing);`})
	_ = agent.RunToolCall(ctx, agent.ToolCall{ID: "example", Name: code.Name, Arguments: raw}, options)
	// JavaScript calls tools.<bindings[i].ExportedName()>; do not guess aliases.
	// This JS runtime plus a root is not an OS filesystem sandbox.
}

func ExampleWorkspaceWriteDefinitions() {
	root, err := os.OpenRoot("/host/configured/workspace")
	if err != nil {
		return
	}
	defer root.Close()
	// Explicit read + create + overwrite authority. New replacement inodes have
	// at most 0600 permissions, with no old metadata/hard-link identity preserved.
	// Host must protect staging names from malicious directory writers.
	definitions, err := fstools.WorkspaceWriteDefinitions(root, fstools.WorkspaceWriteOptions{
		Create:  fstools.CreateOptions{MaxBytes: 32 << 10},
		Replace: fstools.ReplaceOptions{MaxBytes: 32 << 10},
		Edit:    fstools.EditOptions{MaxBytes: 32 << 10},
	})
	if err != nil {
		return
	}
	// This example grants only the selected create call. A real host must check
	// tenant identity, current approvals and arguments on every permission check.
	check := func(_ context.Context, in agent.BeforeToolCallContext) error {
		if in.ToolCall.Name != "create_file" {
			return errors.New("write not authorized")
		}
		return nil
	}
	outcome, receipts, err := agent.RunToolCallWithReceipts(context.Background(), agent.ToolCall{ID: "create-example", Name: "create_file", Arguments: json.RawMessage(`{"path":"example-new.txt","content":"hello"}`)}, agent.RunToolCallOptions{Tools: definitions, CheckToolPermission: check}, 8)
	if err != nil {
		return
	}
	// On failure, inspect the handle before deciding what to do. Never infer
	// that a failed outcome means no file was published, and never blind-retry.
	if outcome.Err != nil {
		facts := receipts.Snapshot()
		_ = facts // Route facts to trusted host reconciliation, not blind retry.
		return
	}
	if err := receipts.Wait(context.Background()); err != nil {
		return
	}
	// Models use Runner.RunWithReceipts with a nil journal instead. Existing
	// Runner/session/checkpoint entry points do not gain receipt support.
	// For create-only authority, compose ReadOnlyDefinitions + CreateFileDefinition
	// instead. Do not select the overwrite preset and rely on its name as a guard.
}
