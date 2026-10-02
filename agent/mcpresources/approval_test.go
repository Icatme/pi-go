package mcpresources

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent"
	managed "github.com/Icatme/pi-go/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func changeResourceScope(ctx context.Context, manager *managed.Manager, change string) error {
	switch change {
	case "scope", "scope_ABA":
		old := manager.Scope()
		if err := manager.SetScope(managed.Scope{Identity: "bob", AuthEpoch: 2}); err != nil {
			return err
		}
		if change == "scope_ABA" {
			return manager.SetScope(old)
		}
		return nil
	case "hidden":
		return manager.SetExposure("fixture", managed.Hidden, nil)
	case "reconnect":
		_, err := manager.Reconnect(ctx, "fixture")
		return err
	default:
		panic("unknown resource scope change")
	}
}

func assertResourceCompletionRejected(t *testing.T, result agent.ToolResult, err error) {
	t.Helper()
	var failure *agent.ToolExecutionError
	if !errors.As(err, &failure) || failure.Code != agent.ToolFailurePolicyDenied || failure.Reason != "scope_or_exposure_changed" {
		t.Fatalf("scope change lost its policy classification: failure=%+v err=%v", failure, err)
	}
	info := failure.Execution
	if info.Remote != agent.ToolRemoteCompleteReported || len(info.Attempts) != 1 || info.Attempts[0].Remote != agent.ToolRemoteCompleteReported {
		t.Fatalf("completed resource read lost its dispatch facts: %+v", info)
	}
	if len(result.Content) != 0 || len(result.StructuredContent) != 0 || result.Details != nil {
		t.Fatalf("rejected resource published old data: %+v", result)
	}
}

func TestResourcePublicationRechecksAfterFinalApproval(t *testing.T) {
	for _, change := range []string{"scope", "hidden", "scope_ABA", "reconnect"} {
		t.Run(change, func(t *testing.T) {
			server, reads := resourceServer(t, "demo://secret", "text/plain", []*sdk.ResourceContents{{Text: "SECRET_OLD_RESOURCE_ACCOUNT"}})
			manager, _ := resourceFixture(t, server, managed.Codemode, false)
			definition := resourceDefinition(t, manager, Options{}, ReadResourceName)
			var checks atomic.Int32
			result, err := invokeResource(t.Context(), definition, map[string]any{"server": "fixture", "uri": "demo://secret"}, func(ctx context.Context) error {
				if checks.Add(1) == 5 {
					return changeResourceScope(ctx, manager, change)
				}
				return nil
			})
			if checks.Load() != 5 || reads.Load() != 1 {
				t.Fatalf("fixture did not reach the final approval: checks=%d reads=%d err=%v", checks.Load(), reads.Load(), err)
			}
			assertResourceCompletionRejected(t, result, err)
			if result.Execution == nil || result.Execution.Remote != agent.ToolRemoteCompleteReported {
				t.Fatalf("local publication rejection lost successful remote execution: %+v", result.Execution)
			}
		})
	}
}

func TestAgentResourcePublicationRechecksAfterFinalApproval(t *testing.T) {
	for _, name := range []string{ReadResourceName, ListResourcesName, ListResourceTemplatesName} {
		t.Run(name, func(t *testing.T) {
			server, _ := resourceServer(t, "demo://secret", "text/plain", []*sdk.ResourceContents{{Text: "SECRET_OLD_RESOURCE_ACCOUNT"}})
			server.AddResourceTemplate(&sdk.ResourceTemplate{Name: "SECRET_TEMPLATE", URITemplate: "demo://secret/{id}"}, func(context.Context, *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
				return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{Text: "secret"}}}, nil
			})
			manager, _ := resourceFixture(t, server, managed.Codemode, false)
			definition := resourceDefinition(t, manager, Options{}, name)
			args := map[string]any{"server": "fixture"}
			last := int32(5) // Agent check, two adapter checks, physical handoff, publication.
			if name == ReadResourceName {
				args["uri"] = "demo://secret"
				last++ // Read checks permission before conversion as well.
			}
			var checks atomic.Int32
			out := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "resource", Name: name, ParsedArgs: args}, agent.RunToolCallOptions{
				Tools: []agent.ToolDefinition{definition},
				CheckToolPermission: func(ctx context.Context, _ agent.BeforeToolCallContext) error {
					if checks.Add(1) == last {
						return changeResourceScope(ctx, manager, "scope")
					}
					return nil
				},
			})
			if checks.Load() != last || !out.IsError || manager.Scope().Identity != "bob" {
				t.Fatalf("Agent fixture missed publication rejection: checks=%d scope=%+v outcome=%+v", checks.Load(), manager.Scope(), out)
			}
			var failure *agent.ToolExecutionError
			if !errors.As(out.Err, &failure) || failure.Code != agent.ToolFailurePolicyDenied || out.Execution.Remote != agent.ToolRemoteCompleteReported || len(out.Execution.Attempts) != 1 || out.Execution.Attempts[0].Remote != agent.ToolRemoteCompleteReported {
				t.Fatalf("Agent lost policy or completion facts: outcome=%+v failure=%+v", out, failure)
			}
			if len(out.Result.StructuredContent) != 0 || out.Result.Details != nil {
				t.Fatalf("Agent published the old structured resource: %+v", out.Result)
			}
			for _, part := range out.Result.Content {
				if part.Type != agent.PartTypeText || strings.Contains(part.Text, "SECRET_") || strings.Contains(part.Text, "demo://secret") {
					t.Fatalf("Agent published old resource content: %+v", out.Result.Content)
				}
			}
		})
	}
}

func TestArtifactSinkRechecksAfterApproval(t *testing.T) {
	for _, change := range []string{"scope", "hidden", "scope_ABA", "reconnect"} {
		t.Run(change, func(t *testing.T) {
			server, reads := resourceServer(t, "demo://secret", "application/octet-stream", []*sdk.ResourceContents{{Blob: []byte("secret")}})
			manager, _ := resourceFixture(t, server, managed.Codemode, false)
			var writes atomic.Int32
			sink := artifactSinkFunc(func(_ context.Context, artifact ResourceArtifact) (ArtifactDescriptor, error) {
				writes.Add(1)
				return ArtifactDescriptor{
					ID: "artifact", Scope: artifact.Scope, ScopeKey: artifactScopeKey(artifact.Scope),
					Server: artifact.Server, URI: artifact.URI, MIMEType: artifact.MIMEType,
					SHA256: artifact.SHA256, Bytes: len(artifact.Data),
				}, nil
			})
			definition := resourceDefinition(t, manager, Options{ArtifactSink: sink}, ReadResourceName)
			var checks atomic.Int32
			result, err := invokeResource(t.Context(), definition, map[string]any{"server": "fixture", "uri": "demo://secret"}, func(ctx context.Context) error {
				if checks.Add(1) == 5 {
					return changeResourceScope(ctx, manager, change)
				}
				return nil
			})
			if checks.Load() != 5 || reads.Load() != 1 || writes.Load() != 0 {
				t.Fatalf("revoked artifact approval reached the sink: checks=%d reads=%d writes=%d err=%v", checks.Load(), reads.Load(), writes.Load(), err)
			}
			assertResourceCompletionRejected(t, result, err)
		})
	}
}

func TestResourceApprovalCancellationRejectsCompletedResult(t *testing.T) {
	for _, artifact := range []bool{false, true} {
		name := "publication"
		contents := []*sdk.ResourceContents{{Text: "SECRET"}}
		mimeType := "text/plain"
		if artifact {
			name = "artifact"
			contents = []*sdk.ResourceContents{{Blob: []byte("secret")}}
			mimeType = "application/octet-stream"
		}
		t.Run(name, func(t *testing.T) {
			server, _ := resourceServer(t, "demo://secret", mimeType, contents)
			manager, _ := resourceFixture(t, server, managed.Codemode, false)
			var writes atomic.Int32
			definition := resourceDefinition(t, manager, Options{ArtifactSink: artifactSinkFunc(func(context.Context, ResourceArtifact) (ArtifactDescriptor, error) {
				writes.Add(1)
				return ArtifactDescriptor{}, errors.New("unexpected artifact write")
			})}, ReadResourceName)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var checks atomic.Int32
			result, err := invokeResource(ctx, definition, map[string]any{"server": "fixture", "uri": "demo://secret"}, func(context.Context) error {
				if checks.Add(1) == 5 {
					cancel()
				}
				return nil
			})
			var failure *agent.ToolExecutionError
			if checks.Load() != 5 || !errors.Is(err, context.Canceled) || !errors.As(err, &failure) || failure.Code != agent.ToolFailureCanceled || failure.Execution.Remote != agent.ToolRemoteCompleteReported || len(failure.Execution.Attempts) != 1 || writes.Load() != 0 || len(result.Content) != 0 || len(result.StructuredContent) != 0 {
				t.Fatalf("canceled completed read leaked data or lost facts: checks=%d writes=%d err=%v result=%+v failure=%+v", checks.Load(), writes.Load(), err, result, failure)
			}
		})
	}
}
