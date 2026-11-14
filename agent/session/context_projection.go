package session

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Icatme/pi-go/agent"
)

// ErrContextProjection means the requested model projection cannot safely be
// produced. The original session remains unchanged.
var ErrContextProjection = errors.New("session: unsupported context projection")

// RetainedThinkingPolicy is an explicit host-selected policy, not model detection.
type RetainedThinkingPolicy string

const (
	PreserveRetainedThinking RetainedThinkingPolicy = ""
	// DropPrefixBoundAnthropicThinking applies only to host-generated compaction
	// summaries and a target the host knows performs Anthropic prefix checks.
	DropPrefixBoundAnthropicThinking RetainedThinkingPolicy = "drop_prefix_bound_anthropic_thinking"
)

// ContextProjectionOptions applies to the resolved target of the next request.
// Re-select it after dynamic model resolution. It is not persisted or inferred
// from a model name, and does not configure server-side block_binding or retries.
type ContextProjectionOptions struct {
	RetainedThinking RetainedThinkingPolicy
	TargetAPI        string
	TargetProvider   string
	TargetModel      string
}

// ProjectContext returns a detached, model-facing view. Context, PrepareRun,
// journals and custom StreamModels do not automatically use this opt-in view.
// Hosts must not write the projected messages back as replacement audit history.
func (s *Session) ProjectContext(lane string, options ContextProjectionOptions) (Context, error) {
	state, err := s.State()
	if err != nil {
		return Context{}, err
	}
	return state.ProjectContext(lane, options)
}

// ProjectContext preserves ordinary Context behavior by default. With the
// explicit Anthropic policy it removes thinking (including redacted thinking)
// only from matching assistants in the newest compaction's retained tail.
// Later messages and known other model identities are left unchanged. This is
// not a projection for native API-generated compaction blocks.
func (s State) ProjectContext(lane string, options ContextProjectionOptions) (Context, error) {
	if options.RetainedThinking == PreserveRetainedThinking {
		return s.Context(lane)
	}
	if options.RetainedThinking != DropPrefixBoundAnthropicThinking || options.TargetAPI != "anthropic-messages" || strings.TrimSpace(options.TargetProvider) == "" || strings.TrimSpace(options.TargetModel) == "" {
		return Context{}, fmt.Errorf("%w: explicit Anthropic target and policy required", ErrContextProjection)
	}
	branch, err := s.Branch(lane)
	if err != nil {
		return Context{}, err
	}
	for i := len(branch) - 1; i >= 0; i-- {
		compaction := branch[i].Compaction
		if branch[i].Type != EntryTypeCompaction || compaction == nil {
			continue
		}
		for j := range compaction.RetainedTail {
			message := &compaction.RetainedTail[j]
			if message.Role != agent.RoleAssistant || !hasThinkingPart(message.Parts) {
				continue
			}
			if strings.TrimSpace(message.API) == "" || strings.TrimSpace(message.Provider) == "" || strings.TrimSpace(message.Model) == "" {
				return Context{}, fmt.Errorf("%w: retained assistant %d has incomplete source identity", ErrContextProjection, j)
			}
			if message.API != options.TargetAPI || message.Provider != options.TargetProvider || message.Model != options.TargetModel {
				continue
			}
			parts := make([]agent.Part, 0, len(message.Parts))
			usable := len(message.ToolCalls) != 0
			for _, part := range message.Parts {
				switch part.Type {
				case agent.PartTypeThinking:
					continue
				case agent.PartTypeText:
					usable = usable || strings.TrimSpace(part.Text) != ""
				case agent.PartTypeImage:
					// Preserve the part, but the Anthropic assistant converter
					// does not emit images. It cannot keep this turn on the wire.
				default:
					return Context{}, fmt.Errorf("%w: retained assistant %d has an unknown part type", ErrContextProjection, j)
				}
				parts = append(parts, part)
			}
			if !usable {
				return Context{}, fmt.Errorf("%w: retained assistant %d would have no usable content or tools", ErrContextProjection, j)
			}
			message.Parts = parts
		}
		break
	}
	return contextFromBranch(branch), nil
}

func hasThinkingPart(parts []agent.Part) bool {
	for _, part := range parts {
		if part.Type == agent.PartTypeThinking {
			return true
		}
	}
	return false
}
