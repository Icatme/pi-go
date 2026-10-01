package agent

import (
	"context"
	"fmt"
)

func finishAgentTurn(ctx context.Context, definition AgentDefinition, turn AgentTurnContext, emit EventSink, state *loopRuntimeState) (decision AgentTurnDecision, err error) {
	defer func() {
		emitEvent(emit, AgentEvent{Type: EventTurnEnd, Message: &turn.Message, ToolMessages: turn.ToolResults})
		state.turnEnded = true
	}()
	if definition.FinishTurn == nil {
		return AgentTurnDecision{}, nil
	}
	input := AgentTurnContext{Message: cloneMessage(turn.Message), ToolResults: cloneMessages(turn.ToolResults), Context: cloneAgentContext(turn.Context), NewMessages: cloneMessages(turn.NewMessages)}
	decision, err = definition.FinishTurn(ctx, input)
	if err != nil {
		return AgentTurnDecision{}, err
	}
	if isErrorAssistantMessage(turn.Message) {
		return AgentTurnDecision{}, nil
	}
	switch decision.Action {
	case "", TurnActionEnd, TurnActionContinue:
		return decision, nil
	default:
		return AgentTurnDecision{}, fmt.Errorf("agent: invalid FinishTurn action %q", decision.Action)
	}
}
