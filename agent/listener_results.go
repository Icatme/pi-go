package agent

// listenerResultBatch is an in-memory rescue buffer, not a durable journal.
// It is used only by explicitly sequential Agent runs; Engine and parallel
// event sinks retain their existing panic contracts.
type listenerResultBatch struct {
	owner     chan struct{}
	calls     []ToolCall
	results   map[string]Message
	committed map[string]bool
}

func (a *Agent) newListenerResultBatchLocked(message Message) {
	a.listenerResults = nil
	if !a.running || a.activeDefinition.ToolExecution != ToolExecutionSequential || len(message.ToolCalls) == 0 {
		return
	}
	b := &listenerResultBatch{owner: a.idle, calls: cloneToolCalls(message.ToolCalls), results: make(map[string]Message), committed: make(map[string]bool)}
	seen := make(map[string]bool)
	for _, call := range b.calls {
		if call.ID == "" || seen[call.ID] {
			return
		}
		seen[call.ID] = true
	}
	a.listenerResults = b
}

func (a *Agent) initializeListenerResultsLocked() {
	for i := len(a.snapshot.Messages) - 1; i >= 0; i-- {
		if a.snapshot.Messages[i].Role != RoleAssistant {
			continue
		}
		a.newListenerResultBatchLocked(a.snapshot.Messages[i])
		for _, message := range a.snapshot.Messages[i+1:] {
			a.markListenerResultCommittedLocked(message)
		}
		return
	}
}

func (a *Agent) markListenerResultCommittedLocked(message Message) {
	b := a.listenerResults
	if b == nil || message.ToolResult == nil {
		return
	}
	for _, call := range b.calls {
		if call.ID == message.ToolResult.ToolCallID && call.Name == message.ToolResult.ToolName {
			b.committed[call.ID] = true
			delete(b.results, call.ID)
			return
		}
	}
}

func (a *Agent) observeListenerResultsLocked(event AgentEvent) {
	if event.Type == EventMessageEnd && event.Message != nil {
		if event.Message.Role == RoleAssistant {
			a.newListenerResultBatchLocked(*event.Message)
		} else {
			a.markListenerResultCommittedLocked(*event.Message)
		}
	}
	b := a.listenerResults
	if b == nil || b.owner != a.idle || !a.running || a.activeDefinition.ToolExecution != ToolExecutionSequential || event.Type != EventToolExecutionEnd || event.ParentToolCallID != "" || event.ToolCall == nil || event.ToolResult == nil {
		return
	}
	for _, call := range b.calls {
		if call.ID == event.ToolCall.ID && call.Name == event.ToolCall.Name && !b.committed[call.ID] {
			// Copy before listeners receive their mutable event copies.
			b.results[call.ID] = NewToolResultMessage(call, *event.ToolResult, event.IsError)
			return
		}
	}
}

func (a *Agent) flushListenerResults(owner chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.listenerResults
	if !a.running || a.idle != owner || b == nil || b.owner != owner || a.activeDefinition.ToolExecution != ToolExecutionSequential {
		return
	}
	for _, call := range b.calls {
		if message, ok := b.results[call.ID]; ok && !b.committed[call.ID] {
			a.appendMessageLocked(message)
			b.committed[call.ID] = true
		}
	}
	a.listenerResults = nil
}
