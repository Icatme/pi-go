package pigo

func cloneModel(model Model) Model {
	cloned := model
	if len(model.Input) > 0 {
		cloned.Input = append([]InputType(nil), model.Input...)
	}
	if len(model.CostTiers) > 0 {
		cloned.CostTiers = append([]ModelCostTier(nil), model.CostTiers...)
	}
	if len(model.ThinkingLevelMap) > 0 {
		cloned.ThinkingLevelMap = make(ThinkingLevelMap, len(model.ThinkingLevelMap))
		for key, value := range model.ThinkingLevelMap {
			cloned.ThinkingLevelMap[key] = value
		}
	}
	if len(model.Headers) > 0 {
		cloned.Headers = make(map[string]string, len(model.Headers))
		for key, value := range model.Headers {
			cloned.Headers[key] = value
		}
	}
	cloned.Compat = cloneCompat(model.Compat)
	return cloned
}

func cloneCompat(compat ProviderCompat) ProviderCompat {
	if compat == nil {
		return nil
	}

	switch typed := compat.(type) {
	case *OpenAICompletionsCompat:
		cloned := *typed
		if typed.OpenRouterRouting != nil {
			routing := *typed.OpenRouterRouting
			routing.Order = cloneStringSlice(typed.OpenRouterRouting.Order)
			routing.Only = cloneStringSlice(typed.OpenRouterRouting.Only)
			routing.Ignore = cloneStringSlice(typed.OpenRouterRouting.Ignore)
			routing.Quantizations = cloneStringSlice(typed.OpenRouterRouting.Quantizations)
			if typed.OpenRouterRouting.MaxPrice != nil {
				routing.MaxPrice = cloneMap(typed.OpenRouterRouting.MaxPrice)
			}
			cloned.OpenRouterRouting = &routing
		}
		if typed.VercelGatewayRouting != nil {
			routing := *typed.VercelGatewayRouting
			routing.Order = cloneStringSlice(typed.VercelGatewayRouting.Order)
			routing.Only = cloneStringSlice(typed.VercelGatewayRouting.Only)
			cloned.VercelGatewayRouting = &routing
		}
		return &cloned
	case *OpenAIResponsesCompat:
		cloned := *typed
		return &cloned
	case *AnthropicMessagesCompat:
		cloned := *typed
		return &cloned
	default:
		return typed
	}
}

func cloneBlocks(blocks []ContentBlock) []ContentBlock {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]ContentBlock, 0, len(blocks))
	for _, block := range blocks {
		switch value := block.(type) {
		case TextContent:
			out = append(out, value)
		case ThinkingContent:
			out = append(out, value)
		case ImageContent:
			out = append(out, value)
		case ToolCall:
			out = append(out, ToolCall{
				ID:               value.ID,
				Name:             value.Name,
				Arguments:        cloneMap(value.Arguments),
				ThoughtSignature: value.ThoughtSignature,
			})
		}
	}
	return out
}

func cloneDiagnostics(diagnostics []AssistantMessageDiagnostic) []AssistantMessageDiagnostic {
	if len(diagnostics) == 0 {
		return nil
	}
	out := make([]AssistantMessageDiagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		cloned := AssistantMessageDiagnostic{
			Type:      diagnostic.Type,
			Timestamp: diagnostic.Timestamp,
			Details:   cloneMap(diagnostic.Details),
		}
		if diagnostic.Error != nil {
			cloned.Error = &DiagnosticErrorInfo{
				Name:    diagnostic.Error.Name,
				Message: diagnostic.Error.Message,
				Stack:   diagnostic.Error.Stack,
				Code:    diagnostic.Error.Code,
			}
		}
		out = append(out, cloned)
	}
	return out
}

func cloneHostedToolExecutions(items []HostedToolExecution) []HostedToolExecution {
	if len(items) == 0 {
		return nil
	}
	out := make([]HostedToolExecution, 0, len(items))
	for _, item := range items {
		out = append(out, HostedToolExecution{
			ID:               item.ID,
			Type:             item.Type,
			Name:             item.Name,
			ProviderToolName: item.ProviderToolName,
			Arguments:        cloneMap(item.Arguments),
			Result:           cloneMap(item.Result),
		})
	}
	return out
}

func cloneMessages(messages []Message) []Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]Message, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			continue
		}
		out = append(out, message.clone())
	}
	return out
}

func cloneMap(values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = cloneAny(value)
	}
	return out
}

func cloneAny(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = cloneAny(item)
		}
		return out
	default:
		return typed
	}
}

func cloneStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func anyString(value any) string {
	text, _ := value.(string)
	return text
}
