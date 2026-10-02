package mcptools

import (
	"context"

	managed "github.com/Icatme/pi-go/mcp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolClient is the protocol surface used by the adapter. Both an SDK session
// and a managed, identity-bound pi-go MCP connection implement it. The latter
// restores schemas and structured results from their real wire snapshots.
type ToolClient interface {
	ListTools(context.Context, *mcp.ListToolsParams) (*mcp.ListToolsResult, error)
	CallTool(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error)
}

type trackedToolClient interface {
	CallToolTracked(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, managed.DispatchRecord, error)
}
