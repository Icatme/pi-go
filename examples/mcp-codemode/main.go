// mcp-codemode demonstrates a caller-owned local MCP session and an ordinary
// Runner. Its deterministic model fixture emits JavaScript without an API key.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/codemodetool"
	"github.com/Icatme/pi-go/agent/mcptools"
	"github.com/Icatme/pi-go/codemode"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
	server := mcp.NewServer(&mcp.Implementation{Name: "local-issues", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{
		Name: "list_issues", Description: "Read one page of local issue fixtures.",
		InputSchema:  map[string]any{"type": "object", "properties": map[string]any{"page": map[string]any{"type": "integer", "minimum": 0, "maximum": 2}}, "required": []string{"page"}, "additionalProperties": false},
		OutputSchema: map[string]any{"type": "object", "properties": map[string]any{"issues": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}}, "required": []string{"issues"}},
	}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Page int `json:"page"`
		}
		if err := json.Unmarshal(request.Params.Arguments, &args); err != nil {
			return nil, err
		}
		rows := make([]map[string]any, 50)
		for i := range rows {
			id := args.Page*50 + i + 1
			rows[i] = map[string]any{"number": id, "title": fmt.Sprintf("Issue %d", id), "open": id%3 == 0, "body": strings.Repeat("fixture detail ", 128)}
		}
		return &mcp.CallToolResult{StructuredContent: map[string]any{"issues": rows}, Content: []mcp.Content{&mcp.TextContent{Text: "50 local issues"}}}, nil
	})
	local, remote := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, remote, nil)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, serverSession.Close()) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "codemode-example", Version: "1"}, &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{}, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
	})
	clientSession, err := client.Connect(ctx, local, nil)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, clientSession.Close()) }()
	leaves, err := mcptools.Discover(ctx, clientSession, mcptools.Options{Names: []string{"list_issues"}})
	if err != nil {
		return err
	}
	config := codemode.DefaultConfig()
	config.Timeout = 5 * time.Minute
	sandbox, err := codemode.NewSandbox(ctx, config)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		runErr = errors.Join(runErr, sandbox.Close(closeCtx))
	}()
	native := agent.ToolDefinition{Name: "label", Description: "Format a count as a short label.", Parameters: map[string]any{
		"type": "object", "properties": map[string]any{"count": map[string]any{"type": "integer", "minimum": 0}}, "required": []string{"count"}, "additionalProperties": false},
		Execute: func(_ context.Context, execution agent.ToolExecutionContext) (agent.ToolResult, error) {
			return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: fmt.Sprintf("%v open issues", execution.Args.(map[string]any)["count"])}}}, nil
		},
	}
	codeTool, err := codemodetool.New(sandbox, []codemodetool.Binding{codemodetool.MCP(leaves[0], "fixture"), codemodetool.Native(native, "local")}, codemodetool.Options{MaxOutputTokens: 256, Timeout: 5 * time.Minute})
	if err != nil {
		return err
	}
	runner, err := agent.NewRunner(agent.AgentDefinition{
		Model: &fixtureModel{}, Tools: []agent.ToolDefinition{codeTool}, MaxTurns: 3,
		CheckToolPermission: func(_ context.Context, before agent.BeforeToolCallContext) error {
			switch before.ToolCall.Name {
			case "code", "label", "mcp__fixture__list_issues":
				return nil
			default:
				return errors.New("tool is outside the explicit example allowlist")
			}
		},
	})
	if err != nil {
		return err
	}
	stream := runner.Query(ctx, "Find the open local issues, return their count and the first three titles.")
	// Events must be drained before Wait. The default child events contain bounded
	// summaries; complete rows stay inside the host/VM and never become messages.
	for range stream.Events() {
	}
	snapshot, err := stream.Wait()
	if err != nil {
		return err
	}
	for _, message := range snapshot.Messages {
		if message.Role == agent.RoleTool && message.ToolResult != nil {
			for _, part := range message.ToolResult.Content {
				if part.Type == agent.PartTypeText {
					fmt.Println(part.Text)
				}
			}
			if message.ToolResult.IsError {
				return errors.New("Codemode example failed; inspect host details")
			}
		}
	}
	return nil
}

type fixtureModel struct{ turn int }

func (m *fixtureModel) Stream(_ context.Context, request agent.ModelRequest) (agent.AssistantStream, error) {
	if len(request.Tools) != 1 || request.Tools[0].Name != "code" {
		return nil, errors.New("expected only the code declaration")
	}
	m.turn++
	message := agent.NewTextMessage(agent.RoleAssistant, "The filtered result is ready.")
	message.StopReason = agent.StopReasonStop
	if m.turn == 1 {
		// This is a deterministic stand-in for the model-generated code. Replace
		// only Model with a configured provider to use real generation.
		code := `const pages = await Promise.all([0,1,2].map(page => tools.mcp__fixture__list_issues({page})));
const open = pages.flatMap(page => page.structuredContent.issues).filter(issue => issue.open);
const label = await tools.label({count:open.length});
text({label, issues:open.slice(0,3).map(issue => ({number:issue.number,title:issue.title}))});`
		arguments, _ := json.Marshal(map[string]any{"code": code})
		message.Parts = nil
		message.StopReason = agent.StopReasonToolUse
		message.ToolCalls = []agent.ToolCall{{ID: "example-code", Name: "code", Arguments: arguments}}
	}
	events := make(chan agent.AssistantEvent, 1)
	events <- agent.AssistantEvent{Type: agent.AssistantEventDone, Message: message}
	close(events)
	return &fixtureStream{message: message, events: events}, nil
}

type fixtureStream struct {
	message agent.Message
	events  chan agent.AssistantEvent
}

func (s *fixtureStream) Events() <-chan agent.AssistantEvent { return s.events }
func (s *fixtureStream) Wait() (agent.Message, error)        { return s.message, nil }
func (s *fixtureStream) Close() error                        { return nil }
