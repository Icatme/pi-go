package pigo

import (
	"fmt"
	"time"
)

func formatThrownValue(value any) string {
	if err, ok := value.(error); ok {
		if err.Error() != "" {
			return err.Error()
		}
		return fmt.Sprintf("%T", err)
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", value)
}

func extractDiagnosticError(err any) DiagnosticErrorInfo {
	if e, ok := err.(error); ok {
		info := DiagnosticErrorInfo{
			Message: e.Error(),
		}
		if info.Message == "" {
			info.Message = fmt.Sprintf("%T", e)
		}
		info.Name = fmt.Sprintf("%T", e)
		return info
	}
	return DiagnosticErrorInfo{
		Name:    "ThrownValue",
		Message: formatThrownValue(err),
	}
}

func createAssistantMessageDiagnostic(diagType string, err any, details map[string]any) AssistantMessageDiagnostic {
	info := extractDiagnosticError(err)
	return AssistantMessageDiagnostic{
		Type:      diagType,
		Timestamp: time.Now(),
		Error:     &info,
		Details:   details,
	}
}

func appendAssistantMessageDiagnostic(message *AssistantMessage, diagnostic AssistantMessageDiagnostic) {
	message.Diagnostics = append(message.Diagnostics, diagnostic)
}
