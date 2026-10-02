// mcp-managed demonstrates managed loopback HTTP MCP, lazy tool exposure,
// Codemode batching and a bounded text resource through the ordinary Agent.
// Its deterministic model fixture needs no provider credentials.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/toolset"
	"github.com/Icatme/pi-go/codemode"
	managed "github.com/Icatme/pi-go/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() (runErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var toolCalls, resourceCalls atomic.Int32
	server := sdk.NewServer(&sdk.Implementation{Name: "managed-local-issues", Version: "1"}, &sdk.ServerOptions{Instructions: "Filter issue details in the script; emit only short summaries."})
	server.AddTool(&sdk.Tool{
		Name: "list_issues", Description: "Read one page of local issues.",
		InputSchema: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{"page": map[string]any{"type": "integer", "minimum": 0, "maximum": 2}},
			"required":   []string{"page"},
		},
		OutputSchema: map[string]any{
			"type": "object", "properties": map[string]any{"issues": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}},
			"required": []string{"issues"},
		},
	}, func(_ context.Context, request *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var args struct {
			Page int `json:"page"`
		}
		if err := json.Unmarshal(request.Params.Arguments, &args); err != nil {
			return nil, err
		}
		toolCalls.Add(1)
		rows := make([]map[string]any, 50)
		for i := range rows {
			number := args.Page*50 + i + 1
			rows[i] = map[string]any{
				// This schema permits string IDs, retaining exact values above 2^53.
				"id": fmt.Sprintf("900719925474%04d", number), "title": fmt.Sprintf("Issue %d", number),
				"open": number%3 == 0, "body": strings.Repeat("RAW_FIXTURE_DETAIL ", 128),
			}
		}
		return &sdk.CallToolResult{StructuredContent: map[string]any{"issues": rows}, Content: []sdk.Content{&sdk.TextContent{Text: "50 local issues"}}}, nil
	})
	server.AddTool(&sdk.Tool{Name: "delete_issue", Description: "HIDDEN_FIXTURE_DESCRIPTION", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return nil, errors.New("hidden tool must never execute")
	})
	server.AddResource(&sdk.Resource{URI: "fixture://guide", Name: "guide", Description: "Issue workflow guide", MIMEType: "text/plain"}, func(_ context.Context, request *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		resourceCalls.Add(1)
		return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: request.Params.URI, MIMEType: "text/plain", Text: "Review the first three open issues."}}}, nil
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Handler:           sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	served := make(chan error, 1)
	go func() { served <- httpServer.Serve(listener) }()
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		if err := httpServer.Shutdown(closeCtx); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("shut down local HTTP server: %w", err), httpServer.Close())
		}
		select {
		case err := <-served:
			if !errors.Is(err, http.ErrServerClosed) {
				runErr = errors.Join(runErr, err)
			}
		case <-closeCtx.Done():
			runErr = errors.Join(runErr, closeCtx.Err())
		}
	}()
	scope := managed.Scope{Identity: "local-example-account", AuthEpoch: 1}
	// Own the pool so unused parallel dials cannot outlive session cleanup.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	manager, err := managed.New(managed.Config{Scope: scope, HTTPClient: &http.Client{Transport: transport}, Servers: []managed.ServerConfig{{
		Name: "fixture", URL: "http://" + listener.Addr().String(), Exposure: managed.Codemode, Timeout: 10 * time.Second,
		ToolRules: []managed.ToolRule{{Pattern: "list_issues", Exposure: managed.Deferred}, {Pattern: "delete_issue", Exposure: managed.Hidden}},
	}}})
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		if err := manager.Close(closeCtx); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close MCP manager: %w", err))
		}
	}()
	sandbox, err := codemode.NewSandbox(ctx, codemode.DefaultConfig())
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		if err := sandbox.Close(closeCtx); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close Codemode sandbox: %w", err))
		}
	}()
	tools, err := toolset.New(manager, sandbox, toolset.Options{
		SnapshotScope: func(snapshot agent.AgentSnapshot) (managed.Scope, bool) {
			// A real host reads this from trusted session/branch metadata.
			return scope, snapshot.SessionID == "managed-example"
		},
	})
	if err != nil {
		return err
	}
	runner, err := agent.NewRunner(agent.AgentDefinition{
		SessionID: "managed-example", Model: &fixtureModel{}, ToolResolver: tools.Resolve, MaxTurns: 4,
		CheckToolPermission: func(_ context.Context, before agent.BeforeToolCallContext) error {
			switch before.ToolCall.Name {
			case "code", "tool_search", "mcp__fixture__list_issues", "read_mcp_resource", "list_mcp_resources", "list_mcp_resource_templates":
				return nil
			default:
				return errors.New("tool is outside the explicit example allowlist")
			}
		},
	})
	if err != nil {
		return err
	}
	stream := runner.Query(ctx, "Find the first three open issue titles and read the local workflow guide.")
	for range stream.Events() {
	}
	snapshot, err := stream.Wait()
	if err != nil {
		return err
	}
	if toolCalls.Load() != 3 || resourceCalls.Load() != 1 {
		return fmt.Errorf("unexpected dispatch counts: tools=%d resources=%d", toolCalls.Load(), resourceCalls.Load())
	}
	for _, message := range snapshot.Messages {
		if message.Role == agent.RoleTool && message.ToolResult != nil && message.ToolResult.ToolName == "code" {
			if message.ToolResult.IsError || len(message.ToolResult.Content) == 0 {
				return errors.New("managed Codemode example failed; inspect host details")
			}
			fmt.Println(message.ToolResult.Content[0].Text)
			return nil
		}
	}
	return errors.New("managed example produced no code result")
}

type fixtureModel struct{ turn int }

func (m *fixtureModel) Stream(_ context.Context, request agent.ModelRequest) (agent.AssistantStream, error) {
	m.turn++
	declarations, err := json.Marshal(request.Tools)
	if err != nil {
		return nil, err
	}
	if strings.Contains(string(declarations), "delete_issue") || strings.Contains(string(declarations), "HIDDEN_") {
		return nil, errors.New("hidden fixture metadata reached model declarations")
	}
	for _, message := range request.Messages {
		if message.ToolResult == nil {
			continue
		}
		if message.ToolResult.IsError {
			return nil, errors.New("fixture received a failed tool call")
		}
		for _, part := range message.ToolResult.Content {
			if strings.Contains(part.Text, "RAW_FIXTURE_DETAIL") || strings.Contains(part.Text, "HIDDEN_") {
				return nil, errors.New("unfiltered payload reached model context")
			}
		}
	}
	message := agent.NewTextMessage(agent.RoleAssistant, "The filtered result is ready.")
	message.StopReason = agent.StopReasonStop
	switch m.turn {
	case 1:
		if strings.Contains(string(declarations), "list_issues") {
			return nil, errors.New("indirect MCP schema was preloaded before search")
		}
		message = toolCall("search-issues", "tool_search", map[string]any{"query": "issues", "namespace": "fixture", "limit": 1})
	case 2:
		if !strings.Contains(string(declarations), "mcp__fixture__list_issues") {
			return nil, errors.New("search did not load the deferred current declaration")
		}
		// This fixture stands in for generated code; swap only Model for a real
		// provider to exercise natural language -> script generation.
		code := `if ("mcp__fixture__delete_issue" in tools || await describeTool("mcp__fixture__delete_issue") !== undefined) throw Error("hidden tool visible");
const pages = await Promise.all([0,1,2].map(page => tools.mcp__fixture__list_issues({page})));
const open = pages.flatMap(page => page.structuredContent.issues).filter(issue => issue.open);
const guide = await tools.read_mcp_resource({server:"fixture",uri:"fixture://guide"});
text({openCount:open.length,titles:open.slice(0,3).map(issue => issue.title),guide:guide.contents[0].text,hiddenAbsent:true});`
		message = toolCall("summarize-issues", "code", map[string]any{"code": code})
	}
	events := make(chan agent.AssistantEvent, 1)
	events <- agent.AssistantEvent{Type: agent.AssistantEventDone, Message: message}
	close(events)
	return &fixtureStream{message: message, events: events}, nil
}

func toolCall(id, name string, args map[string]any) agent.Message {
	raw, _ := json.Marshal(args)
	return agent.Message{Role: agent.RoleAssistant, StopReason: agent.StopReasonToolUse, ToolCalls: []agent.ToolCall{{ID: id, Name: name, Arguments: raw}}}
}

type fixtureStream struct {
	message agent.Message
	events  chan agent.AssistantEvent
}

func (s *fixtureStream) Events() <-chan agent.AssistantEvent { return s.events }
func (s *fixtureStream) Wait() (agent.Message, error)        { return s.message, nil }
func (s *fixtureStream) Close() error                        { return nil }
