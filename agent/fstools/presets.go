package fstools

import (
	"os"

	"github.com/Icatme/pi-go/agent"
)

// ReadOnlyOptions configures each fixed member of the explicit read-only preset.
// Zero fields select that tool's existing defaults, not an absence of limits.
type ReadOnlyOptions struct {
	List    Options
	Read    ReadOptions
	Find    FindOptions
	Grep    GrepOptions
	Preview PreviewEditOptions
}

// ReadOnlyDefinitions returns exactly ls, read, find, grep, preview_edit, in that
// order. It is an explicit content-read grant, not an OS sandbox or redaction
// facility. The caller owns root and must keep it open until calls finish.
// Construction does no filesystem I/O; any invalid member yields nil, error.
// Existing Definitions remains ls-only. No new tools join this preset implicitly.
func ReadOnlyDefinitions(root *os.Root, options ReadOnlyOptions) ([]agent.ToolDefinition, error) {
	definitions, err := Definitions(root, options.List)
	if err != nil {
		return nil, err
	}
	for _, factory := range []func() (agent.ToolDefinition, error){
		func() (agent.ToolDefinition, error) { return ReadDefinition(root, options.Read) },
		func() (agent.ToolDefinition, error) { return FindDefinition(root, options.Find) },
		func() (agent.ToolDefinition, error) { return GrepDefinition(root, options.Grep) },
		func() (agent.ToolDefinition, error) { return PreviewEditDefinition(root, options.Preview) },
	} {
		definition, err := factory()
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

// WorkspaceWriteOptions configures the fixed read-and-write workspace preset.
// Selecting this preset expressly enables overwrite authority, not just creation.
type WorkspaceWriteOptions struct {
	ReadOnly ReadOnlyOptions
	Create   CreateOptions
	Replace  ReplaceOptions
	Edit     EditOptions
}

// WorkspaceWriteDefinitions returns the five read-only members followed by
// create_file, atomic_replace, edit. Writes retain their ordinary last-writer-wins
// semantics, metadata changes, and trusted-directory restrictions; not CAS.
// Linux/platform requirements, per-tool permissions and limits remain unchanged.
// No shell, Python, automatic application defaults or shared locking is added.
// On any member error it returns nil, never a partially usable tool set.
func WorkspaceWriteDefinitions(root *os.Root, options WorkspaceWriteOptions) ([]agent.ToolDefinition, error) {
	definitions, err := ReadOnlyDefinitions(root, options.ReadOnly)
	if err != nil {
		return nil, err
	}
	for _, factory := range []func() (agent.ToolDefinition, error){
		func() (agent.ToolDefinition, error) { return CreateFileDefinition(root, options.Create) },
		func() (agent.ToolDefinition, error) { return AtomicReplaceDefinition(root, options.Replace) },
		func() (agent.ToolDefinition, error) { return EditDefinition(root, options.Edit) },
	} {
		definition, err := factory()
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}
