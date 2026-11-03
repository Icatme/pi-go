package fstools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/codemodetool"
	"github.com/Icatme/pi-go/codemode"
)

func TestCodemodeUsesSameRootAndPermission(t *testing.T) {
	dir, _, leaf := fixture(t, Options{})
	if err := os.WriteFile(filepath.Join(dir, "visible"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	sandbox, err := codemode.NewSandbox(t.Context(), codemode.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sandbox.Close(context.Background()) })
	binding := codemodetool.Native(leaf, "workspace")
	code, err := codemodetool.New(sandbox, []codemodetool.Binding{binding}, codemodetool.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"allowed", "denied", "escape"} {
		t.Run(scenario, func(t *testing.T) {
			path := "."
			if scenario == "escape" {
				path = "../"
			}
			source := `text(await tools.` + binding.ExportedName() + `({path:` + fmtJSON(path) + `}));`
			args, _ := json.Marshal(map[string]any{"code": source})
			leafChecks := 0
			outcome := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "outer", Name: code.Name, Arguments: args}, agent.RunToolCallOptions{
				Tools: []agent.ToolDefinition{code},
				CheckToolPermission: func(_ context.Context, call agent.BeforeToolCallContext) error {
					if call.ToolCall.Name != code.Name {
						leafChecks++
						if scenario == "denied" {
							return errors.New("host revoked workspace")
						}
					}
					return nil
				},
			})
			text := ""
			for _, p := range outcome.Result.Content {
				text += p.Text
			}
			if scenario == "allowed" {
				if outcome.Err != nil || leafChecks == 0 || !strings.Contains(text, "visible") {
					t.Fatalf("checks %d result %s err %v", leafChecks, text, outcome.Err)
				}
			} else {
				if outcome.Err == nil && !outcome.IsError {
					t.Fatalf("unauthorized call succeeded: %s", text)
				}
				if strings.Contains(text, "visible") {
					t.Fatal("denied data leaked")
				}
			}
		})
	}
}
func fmtJSON(value string) string { raw, _ := json.Marshal(value); return string(raw) }
