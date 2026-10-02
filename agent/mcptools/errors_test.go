package mcptools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
	managed "github.com/Icatme/pi-go/mcp"
)

func TestPermissionFailureKeepsClassAndTransportFacts(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code agent.ToolFailureCode
	}{
		{"catalog", managed.ErrStale, agent.ToolFailureSchemaChanged},
		{"canceled", context.Canceled, agent.ToolFailureCanceled},
		{"deadline", context.DeadlineExceeded, agent.ToolFailureDeadline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cause := &agent.ToolExecutionError{
				Code: agent.ToolFailureSchemaChanged, Reason: "catalog_changed", Message: "private policy explanation", Err: tc.err,
				Execution: agent.ToolExecutionInfo{Remote: agent.ToolRemoteCompleteReported, Attempts: []agent.ToolSendAttempt{{Number: 9, Remote: agent.ToolRemoteCompleteReported}}},
			}
			for _, phase := range []string{"before_call", "handoff"} {
				var err error
				if phase == "before_call" {
					err = permissionError(cause)
				} else {
					err = trackedCallError(&managed.DispatchDeniedError{Err: cause}, agent.ToolExecutionInfo{Remote: agent.ToolRemoteNotDispatched})
				}
				var failure *agent.ToolExecutionError
				if !errors.As(err, &failure) || !errors.Is(err, tc.err) || failure.Code != tc.code || failure.Reason != "catalog_changed" {
					t.Fatalf("%s lost trusted classification or cause: failure=%+v err=%v", phase, failure, err)
				}
				if failure.Execution.Remote != agent.ToolRemoteNotDispatched || len(failure.Execution.Attempts) != 0 {
					t.Fatalf("%s adopted callback execution claims: %+v", phase, failure.Execution)
				}
				if phase == "handoff" && strings.Contains(err.Error(), "private policy explanation") {
					t.Fatal("dispatch denial exposed private callback text")
				}
			}
		})
	}
}
