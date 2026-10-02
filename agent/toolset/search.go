package toolset

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/codemode"
	managed "github.com/Icatme/pi-go/mcp"
)

func (t *Toolset) searchTool() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        t.options.SearchName,
		Description: "Search allowed tools not yet declared to the model by topic, optionally within a namespace. Matching tools are loaded as direct declarations for later calls; Codemode can call them without loading. Hidden tools are unavailable.",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{
				"query":     map[string]any{"type": "string", "maxLength": 4096},
				"namespace": map[string]any{"type": "string", "maxLength": 128},
				"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": t.options.MaxSearchResults},
			},
			"required": []string{"query"},
		},
		ExecutionMode: agent.ToolExecutionSequential,
		Execute: func(ctx context.Context, execution agent.ToolExecutionContext) (agent.ToolResult, error) {
			args, ok := execution.Args.(map[string]any)
			if !ok {
				return agent.ToolResult{}, searchArgumentsError()
			}
			query, ok := args["query"].(string)
			if !ok || len(query) > 4096 {
				return agent.ToolResult{}, searchArgumentsError()
			}
			namespace, _ := args["namespace"].(string)
			if len(namespace) > 128 {
				return agent.ToolResult{}, searchArgumentsError()
			}
			limit := t.options.MaxSearchResults
			if value, ok := args["limit"]; ok {
				raw, err := json.Marshal(value)
				if err != nil {
					return agent.ToolResult{}, searchArgumentsError()
				}
				limit, err = strconv.Atoi(string(raw))
				if err != nil || limit < 1 || limit > t.options.MaxSearchResults {
					return agent.ToolResult{}, searchArgumentsError()
				}
			}
			if execution.CheckPermission != nil {
				if err := execution.CheckPermission(ctx); err != nil {
					return agent.ToolResult{}, directoryError(err)
				}
			}
			values, _, frozen, err := t.directory(ctx, true, namespace, nil)
			if err != nil {
				return agent.ToolResult{}, directoryError(err)
			}
			active := make(map[string]bool, len(execution.Context.Tools))
			for _, tool := range execution.Context.Tools {
				active[tool.Name] = true
			}
			summaries := make([]codemode.ToolSummary, 0, len(values))
			for _, value := range values {
				name := value.binding.ExportedName()
				if value.exposure == managed.Direct || active[name] {
					continue
				}
				summaries = append(summaries, codemode.ToolSummary{Name: name, Namespace: value.binding.Namespace, Description: value.binding.Tool.Description})
			}
			matches, err := codemode.SearchTools(summaries, query, codemode.SearchOptions{Namespace: namespace, Limit: limit})
			if err != nil {
				return agent.ToolResult{}, searchArgumentsError()
			}
			names := make([]string, len(matches))
			for i := range matches {
				names[i] = matches[i].Name
				matches[i].Description = safeText(matches[i].Description, 192)
			}
			body, err := json.Marshal(matches)
			if err != nil || len(body) > 32<<10 {
				return agent.ToolResult{}, &agent.ToolExecutionError{Code: agent.ToolFailureResource, Message: "Tool search output exceeds its bounded result limit"}
			}
			if err := ctx.Err(); err != nil {
				return agent.ToolResult{}, &agent.ToolExecutionError{Message: "Tool search canceled", Err: err}
			}
			if execution.CheckPermission != nil {
				if err := execution.CheckPermission(ctx); err != nil {
					return agent.ToolResult{}, directoryError(err)
				}
			}
			// Approval can wait while exposure or directory notifications retire
			// this connection. Recheck the captured generation after approval,
			// before publishing names/descriptions or creating a selection.
			if err := frozen.check(t.manager); err != nil {
				return agent.ToolResult{}, directoryError(err)
			}
			if len(names) == 0 {
				return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "No allowed tools matched. Try another topic or namespace."}}}, nil
			}
			var token [16]byte
			if _, err := rand.Read(token[:]); err != nil {
				return agent.ToolResult{}, &agent.ToolExecutionError{Code: agent.ToolFailureResource, Message: "Tool selection could not be recorded", Err: err}
			}
			id := hex.EncodeToString(token[:])
			t.mu.Lock()
			if err := frozen.check(t.manager); err != nil {
				t.mu.Unlock()
				return agent.ToolResult{}, directoryError(err)
			}
			if t.scope != frozen.scope {
				t.scope = frozen.scope
				t.loaded = make(map[string]loadedSelection)
			}
			if len(t.loaded) >= t.options.MaxSelectionRecords {
				t.mu.Unlock()
				return agent.ToolResult{}, &agent.ToolExecutionError{Code: agent.ToolFailureResource, Message: "Tool selection record limit exceeded; use a new Toolset for the next conversation"}
			}
			t.loaded[id] = loadedSelection{scope: frozen.scope, names: append([]string(nil), names...)}
			t.mu.Unlock()
			return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "Loaded matching tools for later calls:\n" + string(body)}}, Details: selectionReport{SelectionID: id, Names: names}}, nil
		},
	}
}

func searchArgumentsError() error {
	return &agent.ToolExecutionError{Code: agent.ToolFailureArgumentInvalid,
		Message:   "tool_search expects query:string and optional namespace:string, limit:positive integer",
		Execution: agent.ToolExecutionInfo{Local: agent.ToolLocalNotStarted, Remote: agent.ToolRemoteNotDispatched}}
}
