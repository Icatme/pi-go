package pigo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

type contextJSON struct {
	SystemPrompt string           `json:"systemPrompt,omitempty"`
	Messages     []messageJSON    `json:"messages,omitempty"`
	Tools        []toolJSON       `json:"tools,omitempty"`
	HostedTools  []hostedToolJSON `json:"hostedTools,omitempty"`
}

type toolJSON struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type messageJSON struct {
	Role                 string                           `json:"role"`
	Content              json.RawMessage                  `json:"content,omitempty"`
	Timestamp            string                           `json:"timestamp,omitempty"`
	API                  API                              `json:"api,omitempty"`
	Provider             Provider                         `json:"provider,omitempty"`
	Model                string                           `json:"model,omitempty"`
	ResponseModel        string                           `json:"responseModel,omitempty"`
	ResponseID           string                           `json:"responseId,omitempty"`
	HostedToolExecutions []hostedToolExecutionJSON        `json:"hostedToolExecutions,omitempty"`
	Usage                Usage                            `json:"usage,omitempty"`
	StopReason           StopReason                       `json:"stopReason,omitempty"`
	ErrorMessage         string                           `json:"errorMessage,omitempty"`
	Diagnostics          []assistantMessageDiagnosticJSON `json:"diagnostics,omitempty"`
	ToolCallID           string                           `json:"toolCallId,omitempty"`
	ToolName             string                           `json:"toolName,omitempty"`
	Details              any                              `json:"details,omitempty"`
	IsError              bool                             `json:"isError,omitempty"`
}

type hostedToolJSON struct {
	Type HostedToolType `json:"type"`
	Name string         `json:"name,omitempty"`
}

type hostedToolExecutionJSON struct {
	ID               string         `json:"id,omitempty"`
	Type             HostedToolType `json:"type,omitempty"`
	Name             string         `json:"name,omitempty"`
	ProviderToolName string         `json:"providerToolName,omitempty"`
	Arguments        map[string]any `json:"arguments,omitempty"`
	Result           map[string]any `json:"result,omitempty"`
}

type assistantMessageDiagnosticJSON struct {
	Type      string               `json:"type"`
	Timestamp string               `json:"timestamp,omitempty"`
	Error     *diagnosticErrorJSON `json:"error,omitempty"`
	Details   map[string]any       `json:"details,omitempty"`
}

type diagnosticErrorJSON struct {
	Name    string `json:"name,omitempty"`
	Message string `json:"message,omitempty"`
	Stack   string `json:"stack,omitempty"`
	Code    any    `json:"code,omitempty"`
}

type contentBlockJSON struct {
	Type              string         `json:"type"`
	Text              string         `json:"text,omitempty"`
	TextSignature     string         `json:"textSignature,omitempty"`
	Thinking          string         `json:"thinking,omitempty"`
	ThinkingSignature string         `json:"thinkingSignature,omitempty"`
	Redacted          bool           `json:"redacted,omitempty"`
	Data              string         `json:"data,omitempty"`
	MIMEType          string         `json:"mimeType,omitempty"`
	ID                string         `json:"id,omitempty"`
	Name              string         `json:"name,omitempty"`
	Arguments         map[string]any `json:"arguments,omitempty"`
	ThoughtSignature  string         `json:"thoughtSignature,omitempty"`
}

func SerializeContext(context Context) ([]byte, error) {
	return json.Marshal(context)
}

func DeserializeContext(payload []byte) (Context, error) {
	var context Context
	if err := json.Unmarshal(payload, &context); err != nil {
		return Context{}, err
	}
	return context, nil
}

func (context Context) MarshalJSON() ([]byte, error) {
	wire := contextJSON{
		SystemPrompt: context.SystemPrompt,
		Messages:     make([]messageJSON, 0, len(context.Messages)),
		Tools:        make([]toolJSON, 0, len(context.Tools)),
		HostedTools:  make([]hostedToolJSON, 0, len(context.HostedTools)),
	}

	for _, message := range context.Messages {
		encoded, err := marshalMessageJSON(message)
		if err != nil {
			return nil, err
		}
		wire.Messages = append(wire.Messages, encoded)
	}

	for _, tool := range context.Tools {
		wire.Tools = append(wire.Tools, toolJSON{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  cloneAny(tool.Parameters),
		})
	}
	for _, tool := range context.HostedTools {
		wire.HostedTools = append(wire.HostedTools, hostedToolJSON{
			Type: tool.Type,
			Name: tool.Name,
		})
	}

	return json.Marshal(wire)
}

func (context *Context) UnmarshalJSON(payload []byte) error {
	var wire contextJSON
	if err := json.Unmarshal(payload, &wire); err != nil {
		return err
	}

	context.SystemPrompt = wire.SystemPrompt
	context.Messages = make([]Message, 0, len(wire.Messages))
	for _, encoded := range wire.Messages {
		message, err := unmarshalMessageJSON(encoded)
		if err != nil {
			return err
		}
		context.Messages = append(context.Messages, message)
	}

	context.Tools = make([]Tool, 0, len(wire.Tools))
	for _, tool := range wire.Tools {
		context.Tools = append(context.Tools, Tool{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  cloneAny(tool.Parameters),
		})
	}

	context.HostedTools = make([]HostedTool, 0, len(wire.HostedTools))
	for _, tool := range wire.HostedTools {
		context.HostedTools = append(context.HostedTools, HostedTool{
			Type: tool.Type,
			Name: tool.Name,
		})
	}

	return nil
}

func marshalMessageJSON(message Message) (messageJSON, error) {
	switch typed := message.(type) {
	case UserMessage:
		content, err := marshalUserContentJSON(typed.Content)
		if err != nil {
			return messageJSON{}, err
		}
		return messageJSON{
			Role:      "user",
			Content:   content,
			Timestamp: typed.Timestamp.Format(timeLayoutJSON),
		}, nil
	case AssistantMessage:
		content, err := marshalContentBlocksJSON(typed.Content)
		if err != nil {
			return messageJSON{}, err
		}
		return messageJSON{
			Role:                 "assistant",
			Content:              content,
			Timestamp:            typed.Timestamp.Format(timeLayoutJSON),
			API:                  typed.API,
			Provider:             typed.Provider,
			Model:                typed.Model,
			ResponseModel:        typed.ResponseModel,
			ResponseID:           typed.ResponseID,
			HostedToolExecutions: marshalHostedToolExecutionsJSON(typed.HostedToolExecutions),
			Usage:                typed.Usage,
			StopReason:           typed.StopReason,
			ErrorMessage:         typed.ErrorMessage,
			Diagnostics:          marshalAssistantDiagnosticsJSON(typed.Diagnostics),
		}, nil
	case ToolResultMessage:
		content, err := marshalContentBlocksJSON(typed.Content)
		if err != nil {
			return messageJSON{}, err
		}
		return messageJSON{
			Role:       "toolResult",
			Content:    content,
			Timestamp:  typed.Timestamp.Format(timeLayoutJSON),
			ToolCallID: typed.ToolCallID,
			ToolName:   typed.ToolName,
			Details:    cloneAny(typed.Details),
			IsError:    typed.IsError,
		}, nil
	default:
		return messageJSON{}, fmt.Errorf("unsupported message type %T", message)
	}
}

func unmarshalMessageJSON(encoded messageJSON) (Message, error) {
	timestamp, err := parseTimeJSON(encoded.Timestamp)
	if err != nil {
		return nil, err
	}

	switch encoded.Role {
	case "user":
		content, err := unmarshalUserContentJSON(encoded.Content)
		if err != nil {
			return nil, err
		}
		return UserMessage{
			Content:   content,
			Timestamp: timestamp,
		}, nil
	case "assistant":
		content, err := unmarshalContentBlocksJSON(encoded.Content)
		if err != nil {
			return nil, err
		}
		diagnostics, err := unmarshalAssistantDiagnosticsJSON(encoded.Diagnostics)
		if err != nil {
			return nil, err
		}
		return AssistantMessage{
			Content:              content,
			API:                  encoded.API,
			Provider:             encoded.Provider,
			Model:                encoded.Model,
			ResponseModel:        encoded.ResponseModel,
			ResponseID:           encoded.ResponseID,
			HostedToolExecutions: unmarshalHostedToolExecutionsJSON(encoded.HostedToolExecutions),
			Usage:                encoded.Usage,
			StopReason:           encoded.StopReason,
			ErrorMessage:         encoded.ErrorMessage,
			Diagnostics:          diagnostics,
			Timestamp:            timestamp,
		}, nil
	case "toolResult":
		content, err := unmarshalContentBlocksJSON(encoded.Content)
		if err != nil {
			return nil, err
		}
		return ToolResultMessage{
			ToolCallID: encoded.ToolCallID,
			ToolName:   encoded.ToolName,
			Content:    content,
			Details:    cloneAny(encoded.Details),
			IsError:    encoded.IsError,
			Timestamp:  timestamp,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported message role %q", encoded.Role)
	}
}

func marshalHostedToolExecutionsJSON(items []HostedToolExecution) []hostedToolExecutionJSON {
	wire := make([]hostedToolExecutionJSON, 0, len(items))
	for _, item := range items {
		wire = append(wire, hostedToolExecutionJSON{
			ID:               item.ID,
			Type:             item.Type,
			Name:             item.Name,
			ProviderToolName: item.ProviderToolName,
			Arguments:        cloneMap(item.Arguments),
			Result:           cloneMap(item.Result),
		})
	}
	return wire
}

func unmarshalHostedToolExecutionsJSON(items []hostedToolExecutionJSON) []HostedToolExecution {
	result := make([]HostedToolExecution, 0, len(items))
	for _, item := range items {
		result = append(result, HostedToolExecution{
			ID:               item.ID,
			Type:             item.Type,
			Name:             item.Name,
			ProviderToolName: item.ProviderToolName,
			Arguments:        cloneMap(item.Arguments),
			Result:           cloneMap(item.Result),
		})
	}
	return result
}

func marshalAssistantDiagnosticsJSON(items []AssistantMessageDiagnostic) []assistantMessageDiagnosticJSON {
	wire := make([]assistantMessageDiagnosticJSON, 0, len(items))
	for _, item := range items {
		var diagnosticError *diagnosticErrorJSON
		if item.Error != nil {
			diagnosticError = &diagnosticErrorJSON{
				Name:    item.Error.Name,
				Message: item.Error.Message,
				Stack:   item.Error.Stack,
				Code:    cloneAny(item.Error.Code),
			}
		}
		wire = append(wire, assistantMessageDiagnosticJSON{
			Type:      item.Type,
			Timestamp: item.Timestamp.Format(timeLayoutJSON),
			Error:     diagnosticError,
			Details:   cloneMap(item.Details),
		})
	}
	return wire
}

func unmarshalAssistantDiagnosticsJSON(items []assistantMessageDiagnosticJSON) ([]AssistantMessageDiagnostic, error) {
	result := make([]AssistantMessageDiagnostic, 0, len(items))
	for _, item := range items {
		timestamp, err := parseTimeJSON(item.Timestamp)
		if err != nil {
			return nil, err
		}
		var diagnosticError *DiagnosticErrorInfo
		if item.Error != nil {
			diagnosticError = &DiagnosticErrorInfo{
				Name:    item.Error.Name,
				Message: item.Error.Message,
				Stack:   item.Error.Stack,
				Code:    cloneAny(item.Error.Code),
			}
		}
		result = append(result, AssistantMessageDiagnostic{
			Type:      item.Type,
			Timestamp: timestamp,
			Error:     diagnosticError,
			Details:   cloneMap(item.Details),
		})
	}
	return result, nil
}

func marshalUserContentJSON(content any) (json.RawMessage, error) {
	switch typed := content.(type) {
	case nil:
		return json.RawMessage("null"), nil
	case string:
		return json.Marshal(typed)
	case []ContentBlock:
		return marshalContentBlocksJSON(typed)
	default:
		return json.Marshal(cloneAny(typed))
	}
}

func unmarshalUserContentJSON(payload json.RawMessage) (any, error) {
	if len(bytes.TrimSpace(payload)) == 0 || bytes.Equal(bytes.TrimSpace(payload), []byte("null")) {
		return nil, nil
	}

	var text string
	if err := json.Unmarshal(payload, &text); err == nil {
		return text, nil
	}

	content, err := unmarshalContentBlocksJSON(payload)
	if err == nil {
		return content, nil
	}

	var generic any
	if err := json.Unmarshal(payload, &generic); err != nil {
		return nil, err
	}
	return generic, nil
}

func marshalContentBlocksJSON(blocks []ContentBlock) (json.RawMessage, error) {
	if len(blocks) == 0 {
		return json.RawMessage("[]"), nil
	}

	wire := make([]contentBlockJSON, 0, len(blocks))
	for _, block := range blocks {
		switch typed := block.(type) {
		case TextContent:
			wire = append(wire, contentBlockJSON{
				Type:          "text",
				Text:          typed.Text,
				TextSignature: typed.TextSignature,
			})
		case ThinkingContent:
			wire = append(wire, contentBlockJSON{
				Type:              "thinking",
				Thinking:          typed.Thinking,
				ThinkingSignature: typed.ThinkingSignature,
				Redacted:          typed.Redacted,
			})
		case ImageContent:
			wire = append(wire, contentBlockJSON{
				Type:     "image",
				Data:     typed.Data,
				MIMEType: typed.MIMEType,
			})
		case ToolCall:
			wire = append(wire, contentBlockJSON{
				Type:             "toolCall",
				ID:               typed.ID,
				Name:             typed.Name,
				Arguments:        cloneMap(typed.Arguments),
				ThoughtSignature: typed.ThoughtSignature,
			})
		default:
			return nil, fmt.Errorf("unsupported content block type %T", block)
		}
	}

	return json.Marshal(wire)
}

func unmarshalContentBlocksJSON(payload json.RawMessage) ([]ContentBlock, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil, nil
	}

	var wire []contentBlockJSON
	if err := json.Unmarshal(payload, &wire); err != nil {
		return nil, err
	}

	blocks := make([]ContentBlock, 0, len(wire))
	for _, block := range wire {
		switch block.Type {
		case "text":
			blocks = append(blocks, TextContent{
				Text:          block.Text,
				TextSignature: block.TextSignature,
			})
		case "thinking":
			blocks = append(blocks, ThinkingContent{
				Thinking:          block.Thinking,
				ThinkingSignature: block.ThinkingSignature,
				Redacted:          block.Redacted,
			})
		case "image":
			blocks = append(blocks, ImageContent{
				Data:     block.Data,
				MIMEType: block.MIMEType,
			})
		case "toolCall":
			blocks = append(blocks, ToolCall{
				ID:               block.ID,
				Name:             block.Name,
				Arguments:        cloneMap(block.Arguments),
				ThoughtSignature: block.ThoughtSignature,
			})
		default:
			return nil, fmt.Errorf("unsupported content block type %q", block.Type)
		}
	}

	return blocks, nil
}

const timeLayoutJSON = "2006-01-02T15:04:05.999999999Z07:00"

func parseTimeJSON(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	return time.Parse(timeLayoutJSON, value)
}
