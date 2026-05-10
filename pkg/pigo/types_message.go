package pigo

import "time"

type Message interface {
	messageRole() string
	clone() Message
}

type UserMessage struct {
	Content   any
	Timestamp time.Time
}

func (UserMessage) messageRole() string { return "user" }

func (m UserMessage) clone() Message {
	return UserMessage{
		Content:   cloneAny(m.Content),
		Timestamp: m.Timestamp,
	}
}

type AssistantMessage struct {
	Content              []ContentBlock
	HostedToolExecutions []HostedToolExecution
	API                  API
	Provider             Provider
	Model                string
	ResponseModel        string
	ResponseID           string
	Usage                Usage
	StopReason           StopReason
	ErrorMessage         string
	Diagnostics          []AssistantMessageDiagnostic
	Timestamp            time.Time
}

func (AssistantMessage) messageRole() string { return "assistant" }

func (m AssistantMessage) clone() Message {
	return AssistantMessage{
		Content:              cloneBlocks(m.Content),
		HostedToolExecutions: cloneHostedToolExecutions(m.HostedToolExecutions),
		API:                  m.API,
		Provider:             m.Provider,
		Model:                m.Model,
		ResponseModel:        m.ResponseModel,
		ResponseID:           m.ResponseID,
		Usage:                m.Usage,
		StopReason:           m.StopReason,
		ErrorMessage:         m.ErrorMessage,
		Diagnostics:          cloneDiagnostics(m.Diagnostics),
		Timestamp:            m.Timestamp,
	}
}

type ToolResultMessage struct {
	ToolCallID string
	ToolName   string
	Content    []ContentBlock
	Details    any
	IsError    bool
	Timestamp  time.Time
}

func (ToolResultMessage) messageRole() string { return "toolResult" }

func (m ToolResultMessage) clone() Message {
	return ToolResultMessage{
		ToolCallID: m.ToolCallID,
		ToolName:   m.ToolName,
		Content:    cloneBlocks(m.Content),
		Details:    cloneAny(m.Details),
		IsError:    m.IsError,
		Timestamp:  m.Timestamp,
	}
}
