// Package toolset projects managed MCP capabilities into Agent declarations
// and a lazy Codemode catalog. Executable tools are always rebuilt from the
// current trusted manager; history contains only interface/selection data.
package toolset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/codemodetool"
	"github.com/Icatme/pi-go/agent/mcpresources"
	"github.com/Icatme/pi-go/agent/mcptools"
	"github.com/Icatme/pi-go/codemode"
	"github.com/Icatme/pi-go/internal/jsontext"
	managed "github.com/Icatme/pi-go/mcp"
)

type Options struct {
	Native              []codemodetool.Binding
	Code                codemodetool.Options
	Resources           mcpresources.Options
	SearchName          string
	MaxSearchResults    int
	MaxSelectionRecords int
	// SnapshotScope reads identity provenance saved by the trusted host alongside
	// its session/branch. Without it, restoration accepts only successful searches
	// made by this live Toolset and still present on the current transcript branch.
	// A model-supplied server name, argument or result cannot choose a scope.
	SnapshotScope func(agent.AgentSnapshot) (managed.Scope, bool)
}

type loadedSelection struct {
	scope managed.Scope
	names []string
}

type selectionReport struct {
	SelectionID string   `json:"selectionId"`
	Names       []string `json:"names"`
}

type Toolset struct {
	manager *managed.Manager
	sandbox *codemode.Sandbox
	options Options
	mu      sync.Mutex
	scope   managed.Scope
	loaded  map[string]loadedSelection
	code    agent.ToolDefinition
	search  agent.ToolDefinition
}

type entry struct {
	binding  codemodetool.Binding
	exposure managed.Exposure
}

// New performs validation only. Indirect MCP servers are connected when a
// script or tool_search requests their catalog; direct declarations wait for
// the servers required by their direct policy.
func New(manager *managed.Manager, sandbox *codemode.Sandbox, options Options) (*Toolset, error) {
	if manager == nil || sandbox == nil {
		return nil, errors.New("toolset: manager and sandbox are required")
	}
	if options.SearchName == "" {
		options.SearchName = "tool_search"
	}
	if options.MaxSearchResults == 0 {
		options.MaxSearchResults = 5
	}
	if options.MaxSelectionRecords == 0 {
		options.MaxSelectionRecords = 256
	}
	if !identifier(options.SearchName) || len(options.SearchName) > 64 || options.MaxSearchResults < 1 || options.MaxSearchResults > 50 || options.MaxSelectionRecords < 1 || options.MaxSelectionRecords > 1024 {
		return nil, errors.New("toolset: invalid search or selection limit")
	}
	options.Native = append([]codemodetool.Binding(nil), options.Native...)
	options.Code.Namespaces = append([]codemode.Namespace(nil), options.Code.Namespaces...)
	servers := manager.Servers()
	serverNames := make(map[string]bool, len(servers))
	for _, server := range servers {
		serverNames[server.Name] = true
	}
	for i := range options.Native {
		binding := &options.Native[i]
		if binding.Projection != codemodetool.NativeValue || binding.Namespace == "" || serverNames[binding.Namespace] || binding.Namespace == "mcp_resources" {
			return nil, errors.New("toolset: native bindings need distinct native namespaces")
		}
		frozen, err := detachedTool(binding.Tool)
		if err != nil {
			return nil, err
		}
		binding.Tool = frozen
	}
	validationBindings := append([]codemodetool.Binding(nil), options.Native...)
	virtuals, err := mcpresources.Definitions(manager, options.Resources)
	if err != nil {
		return nil, err
	}
	for _, virtual := range virtuals {
		validationBindings = append(validationBindings, codemodetool.Native(virtual, "mcp_resources"))
	}
	for _, binding := range validationBindings {
		if binding.ExportedName() == options.SearchName {
			return nil, errors.New("toolset: search and leaf names conflict")
		}
	}
	if _, err := codemodetool.New(sandbox, validationBindings, options.Code); err != nil {
		return nil, err
	}
	if options.Code.Name != "" && options.Code.Name == options.SearchName || options.Code.Name == "" && options.SearchName == "code" {
		return nil, errors.New("toolset: code and search names conflict")
	}
	t := &Toolset{manager: manager, sandbox: sandbox, options: options, scope: manager.Scope(), loaded: make(map[string]loadedSelection)}
	codeOptions := options.Code
	if codeOptions.Description == "" {
		codeOptions.Description = "Run an async JavaScript body; await tools.<name>(args) or Promise.all. " +
			"Discover allowed tools with ALL_TOOLS, searchTools(query,{namespace?,limit?}), describeTool(name), describeNamespace(name). " +
			"Descriptions explain inputs and resolved values; check availability with \"name\" in tools. " +
			"Globals: text/return, image, console.*, store/load, exit. No direct filesystem, network or timers."
	}
	code, err := codemodetool.NewDynamic(sandbox, func(ctx context.Context) ([]codemodetool.Binding, []codemode.Namespace, error) {
		values, namespaces, _, err := t.directory(ctx, true, "", nil)
		if err != nil {
			return nil, nil, err
		}
		bindings := make([]codemodetool.Binding, len(values))
		for i, value := range values {
			bindings[i] = value.binding
		}
		return bindings, namespaces, nil
	}, codeOptions)
	if err != nil {
		return nil, err
	}
	t.code = code
	t.search = t.searchTool()
	return t, nil
}

// Resolve is suitable for AgentDefinition.ToolResolver. Executable declarations
// loaded by search are reconstructed after connecting the relevant servers,
// including on resume. A script receives its own fixed directory snapshot.
func (t *Toolset) Resolve(ctx context.Context, snapshot agent.AgentSnapshot) ([]agent.ToolDefinition, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	scope := t.manager.Scope()
	selected := t.selected(snapshot, scope)
	values, _, actualScope, err := t.directory(ctx, false, "", selected)
	if err != nil {
		return nil, directoryError(err)
	}
	if actualScope != scope {
		return nil, directoryError(managed.ErrStale)
	}
	out := make([]agent.ToolDefinition, 0, len(values)+2)
	names := make(map[string]bool)
	for _, value := range values {
		name := value.binding.ExportedName()
		if value.exposure != managed.Direct && !selected[name] {
			continue
		}
		definition := aliasTool(value.binding)
		if names[definition.Name] || definition.Name == t.code.Name || definition.Name == t.search.Name {
			return nil, errors.New("toolset: conflicting exported tool names")
		}
		names[definition.Name] = true
		out = append(out, definition)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	out = append(out, t.code, t.search)
	if err := ctx.Err(); err != nil {
		return nil, directoryError(err)
	}
	return out, nil
}

func (t *Toolset) selected(snapshot agent.AgentSnapshot, scope managed.Scope) map[string]bool {
	trustedRestore := false
	if t.options.SnapshotScope != nil {
		stored, ok := t.options.SnapshotScope(snapshot)
		trustedRestore = ok && stored == scope
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.scope != scope {
		t.scope = scope
		t.loaded = make(map[string]loadedSelection)
	}
	out := make(map[string]bool)
	for _, message := range snapshot.Messages {
		if message.Role == agent.RoleSystem && message.System != nil {
			for _, removed := range message.System.ToolsRemoved {
				delete(out, removed.Name)
			}
			if trustedRestore {
				for _, added := range message.System.ToolsAdded {
					out[added.Name] = true
				}
			}
		}
		if message.Role != agent.RoleTool || message.ToolResult == nil || message.ToolResult.ToolName != t.search.Name || message.ToolResult.IsError {
			continue
		}
		var report selectionReport
		raw, err := json.Marshal(message.ToolResult.Details)
		if err != nil || len(raw) > 16<<10 || json.Unmarshal(raw, &report) != nil {
			continue
		}
		// Only the opaque host-generated record selects names. Serialized report
		// names and schema/executor-shaped values never grant a capability.
		selection, ok := t.loaded[report.SelectionID]
		if ok && selection.scope == scope {
			for _, name := range selection.names {
				out[name] = true
			}
		}
	}
	return out
}

func (t *Toolset) directory(ctx context.Context, indirect bool, namespace string, selected map[string]bool) ([]entry, []codemode.Namespace, managed.Scope, error) {
	scope := t.manager.Scope()
	values := make([]entry, 0, len(t.options.Native)+3)
	snapshots := make(map[*managed.Connection]managed.Catalog)
	namespaces := append([]codemode.Namespace(nil), t.options.Code.Namespaces...)
	for _, binding := range t.options.Native {
		if namespace == "" || binding.Namespace == namespace {
			values = append(values, entry{binding: binding, exposure: managed.Direct})
		}
	}
	resources, err := mcpresources.Definitions(t.manager, t.options.Resources)
	if err != nil {
		return nil, nil, scope, err
	}
	for _, resource := range resources {
		if namespace == "" || namespace == "mcp_resources" {
			resource.Revision = scopeRevision(scope)
			values = append(values, entry{binding: codemodetool.Native(resource, "mcp_resources"), exposure: managed.Direct})
		}
	}
	for _, server := range t.manager.Servers() {
		if server.Disabled || server.Exposure == managed.Hidden && len(server.ToolRules) == 0 || namespace != "" && namespace != server.Name {
			continue
		}
		var connection *managed.Connection
		if indirect || server.NeedsDirect() || selectsServer(selected, server.Name) {
			connection, err = t.manager.Connect(ctx, server.Name)
		} else {
			connection, err = t.manager.Ready(server.Name)
			if errors.Is(err, managed.ErrNotReady) {
				continue
			}
		}
		if err != nil {
			return nil, nil, scope, err
		}
		// Policy and schemas must belong to the same connection generation. The
		// manager's earlier enumeration may have changed while another server
		// was connecting.
		server = connection.Config()
		catalog, err := connection.Refresh(ctx)
		if err != nil || catalog.Scope != scope {
			if err == nil {
				err = managed.ErrStale
			}
			return nil, nil, scope, err
		}
		wanted := make([]string, 0, len(catalog.Tools))
		for _, tool := range catalog.Tools {
			if server.ToolExposure(tool.Name) != managed.Hidden {
				wanted = append(wanted, tool.Name)
			}
		}
		if len(wanted) == 0 {
			continue
		}
		definitions, err := mcptools.FromCatalog(connection, catalog.Tools, mcptools.Options{Names: wanted})
		if err != nil {
			return nil, nil, scope, err
		}
		snapshots[connection] = catalog
		for _, definition := range definitions {
			definition.Revision = fmt.Sprintf("%s/%d/%d", scopeRevision(scope), catalog.Generation, catalog.Revision)
			definition = guardCatalog(definition, connection, catalog)
			values = append(values, entry{binding: codemodetool.MCP(definition, server.Name), exposure: server.ToolExposure(definition.Name)})
		}
		namespaces = append(namespaces, codemode.Namespace{Name: server.Name, Description: catalog.Description, Instructions: catalog.Instructions})
	}
	if len(values) > 256 {
		return nil, nil, scope, errors.New("toolset: visible directory exceeds 256 tools; restrict the search namespace or host policy")
	}
	for connection, catalog := range snapshots {
		if !connection.IsCurrent(catalog) {
			return nil, nil, scope, managed.ErrStale
		}
	}
	if t.manager.Scope() != scope {
		return nil, nil, scope, managed.ErrStale
	}
	return values, namespaces, scope, nil
}

func guardCatalog(definition agent.ToolDefinition, connection *managed.Connection, catalog managed.Catalog) agent.ToolDefinition {
	execute := definition.Execute
	current := func() error {
		if connection.IsCurrent(catalog) {
			return nil
		}
		return &agent.ToolExecutionError{
			Code: agent.ToolFailureSchemaChanged, Reason: "catalog_changed", Err: managed.ErrStale,
			Message:   "MCP directory changed; resolve the current tools and approve again",
			Execution: agent.ToolExecutionInfo{Remote: agent.ToolRemoteNotDispatched},
		}
	}
	definition.Execute = func(ctx context.Context, execution agent.ToolExecutionContext) (agent.ToolResult, error) {
		if err := current(); err != nil {
			return agent.ToolResult{}, err
		}
		permission := execution.CheckPermission
		// mcptools passes this check through to the managed transport's actual
		// handoff, including when the host supplied no permission callback.
		execution.CheckPermission = func(ctx context.Context) error {
			if err := current(); err != nil {
				return err
			}
			if permission != nil {
				if err := permission(ctx); err != nil {
					return err
				}
			}
			// Approval can wait while a notification invalidates the catalog.
			return current()
		}
		return execute(ctx, execution)
	}
	return definition
}

func selectsServer(selected map[string]bool, server string) bool {
	p := codemodetool.MCP(agent.ToolDefinition{}, server).NamespacePrefix()
	for name := range selected {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func scopeRevision(scope managed.Scope) string {
	hash := sha256.Sum256([]byte(scope.Identity))
	return fmt.Sprintf("%s/%d", hex.EncodeToString(hash[:8]), scope.AuthEpoch)
}

func aliasTool(binding codemodetool.Binding) agent.ToolDefinition {
	definition := binding.Tool
	definition.Name = binding.ExportedName()
	if binding.Tool.ParseArguments != nil {
		definition.ParseArguments = func(call agent.ToolCall) (any, error) {
			call.Name = binding.Tool.Name
			return binding.Tool.ParseArguments(call)
		}
	}
	definition.Execute = func(ctx context.Context, execution agent.ToolExecutionContext) (agent.ToolResult, error) {
		execution.ToolCall.Name = binding.Tool.Name
		return binding.Tool.Execute(ctx, execution)
	}
	return definition
}

func detachedTool(tool agent.ToolDefinition) (agent.ToolDefinition, error) {
	if err := jsontext.ValidateStrings([]string{tool.Name, tool.Description, tool.Label, tool.Revision}); err != nil {
		return tool, err
	}
	for _, pointer := range []*map[string]any{&tool.Parameters, &tool.OutputSchema} {
		if *pointer == nil {
			continue
		}
		if err := jsontext.ValidateStrings(*pointer); err != nil {
			return tool, err
		}
		raw, err := json.Marshal(*pointer)
		if err != nil || len(raw) > 64<<10 {
			return tool, errors.New("toolset: invalid or oversized native schema")
		}
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		var value map[string]any
		if err := decoder.Decode(&value); err != nil {
			return tool, err
		}
		*pointer = value
	}
	return tool, nil
}

func identifier(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func safeText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit]
}

func directoryError(err error) error {
	code := agent.ToolFailurePolicyDenied
	if errors.Is(err, context.DeadlineExceeded) {
		code = agent.ToolFailureDeadline
	} else if errors.Is(err, context.Canceled) {
		code = agent.ToolFailureCanceled
	}
	return &agent.ToolExecutionError{Code: code, Reason: "directory_unavailable", Message: "MCP directory unavailable; reconnect or reauthenticate through the host", Err: err,
		Execution: agent.ToolExecutionInfo{Local: agent.ToolLocalNotStarted, Remote: agent.ToolRemoteNotDispatched}}
}
