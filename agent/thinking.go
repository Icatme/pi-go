package agent

// ThinkingLevelResolver maps a requested preference to a model's effective level.
// It must be pure, safe for concurrent use, and must not call back into Agent.
// Custom backends preserve the request unchanged unless this resolver is set.
type ThinkingLevelResolver func(ModelRef, ThinkingLevel) ThinkingLevel

func requestedThinkingLevel(level ThinkingLevel) ThinkingLevel {
	if level == "" {
		return ThinkingOff
	}
	return level
}

func (d AgentDefinition) effectiveThinkingLevel(ref ModelRef, requested ThinkingLevel) ThinkingLevel {
	requested = requestedThinkingLevel(requested)
	if d.ThinkingLevelResolver != nil {
		return requestedThinkingLevel(d.ThinkingLevelResolver(cloneModelRef(ref), requested))
	}
	if d.Model != nil || d.ModelResolver != nil {
		return requested
	}
	return resolvePigoThinkingLevel(ref, requested)
}

func initializeThinkingState(definition AgentDefinition, snapshot *AgentSnapshot) AgentDefinition {
	if snapshot.RequestedThinkingLevel != "" {
		definition.ThinkingLevel = snapshot.RequestedThinkingLevel
	}
	updateThinkingState(definition, snapshot)
	return definition
}

func updateThinkingState(definition AgentDefinition, snapshot *AgentSnapshot) {
	ref := effectiveModelRef(*snapshot, definition)
	snapshot.RequestedThinkingLevel = requestedThinkingLevel(definition.ThinkingLevel)
	snapshot.ThinkingLevel = definition.effectiveThinkingLevel(ref, snapshot.RequestedThinkingLevel)
}
