package pigo

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

var errKiroRequestEnvelope = errors.New("kiro: unsupported offline request envelope")

// Explicit caller assertions, not discovered credentials or verified identity.
// Origin has no default: a serializable value is not proof the service accepts it.
type kiroRequestIdentity struct {
	ConversationID string
	ProfileARN     string
	Origin         string
	ModelID        string
}

type kiroRequestLimits struct {
	Messages int
	// Tools limits both catalog entries and total history calls; the
	// underlying history validator additionally caps the catalog at 256.
	Tools        int
	PayloadBytes int
	WireBytes    int
}

type kiroRequestUser struct {
	Content string               `json:"content"`
	ModelID string               `json:"modelId"`
	Origin  string               `json:"origin"`
	Context *kiroToolWireContext `json:"userInputMessageContext,omitempty"`
}

type kiroRequestEntry struct {
	User      *kiroRequestUser       `json:"userInputMessage,omitempty"`
	Assistant *kiroToolWireAssistant `json:"assistantResponseMessage,omitempty"`
}

type kiroRequestEnvelope struct {
	ConversationState struct {
		ChatTriggerType string `json:"chatTriggerType"`
		AgentTaskType   string `json:"agentTaskType"`
		ConversationID  string `json:"conversationId"`
		CurrentMessage  struct {
			User kiroRequestUser `json:"userInputMessage"`
		} `json:"currentMessage"`
		History []kiroRequestEntry `json:"history,omitempty"`
	} `json:"conversationState"`
	ProfileARN string `json:"profileArn"`
	AgentMode  string `json:"agentMode"`
}

func validKiroRequestInputs(id kiroRequestIdentity, limits kiroRequestLimits) bool {
	// Fixed input ceilings bound the admitted work independently of caller limits.
	// WireBytes is an output limit, NOT a hard allocation/heap limit for Marshal.
	if limits.Messages < 1 || limits.Messages > 4096 || limits.Tools < 1 || limits.Tools > 1024 || limits.PayloadBytes < 2 || limits.PayloadBytes > 1<<20 || limits.WireBytes < 2 || limits.WireBytes > 1<<20 {
		return false
	}
	for _, field := range []struct {
		value string
		max   int
	}{
		{id.ConversationID, 128}, {id.ProfileARN, 2048}, {id.Origin, 64}, {id.ModelID, 256},
	} {
		if len(field.value) == 0 || len(field.value) > field.max || !utf8.ValidString(field.value) || strings.TrimSpace(field.value) != field.value || strings.IndexFunc(field.value, unicode.IsControl) >= 0 {
			return false
		}
	}
	return true
}

// encodeKiroOfflineRequest composes a community-schema fixture, not an HTTP
// request. No endpoint, headers, token lookup, identity impersonation default,
// retries or provider registration. Caller inputs must not mutate concurrently.
// System instructions are rejected, never silently lowered. Existing strict
// projection restrictions (including persisted raw tool history) still apply.
func encodeKiroOfflineRequest(ctx Context, schemas map[string]json.RawMessage, id kiroRequestIdentity, limits kiroRequestLimits) ([]byte, error) {
	return encodeKiroOfflineRequestWithPolicy(ctx, schemas, id, limits, kiroToolHistoryOriginal)
}

func encodeKiroOfflineRequestWithPolicy(ctx Context, schemas map[string]json.RawMessage, id kiroRequestIdentity, limits kiroRequestLimits, policy kiroToolHistoryPolicy) ([]byte, error) {
	if policy != kiroToolHistoryOriginal && policy != kiroToolHistoryAuthored {
		return nil, errKiroRequestEnvelope
	}
	if !validKiroRequestInputs(id, limits) || ctx.SystemPrompt != "" {
		return nil, errKiroRequestEnvelope
	}
	var request kiroRequestEnvelope
	request.ConversationState.ChatTriggerType = "MANUAL"
	request.ConversationState.AgentTaskType = "vibe"
	request.ConversationState.ConversationID = id.ConversationID
	request.ProfileARN = id.ProfileARN
	request.AgentMode = "vibe"
	user := func(content, model string, context *kiroToolWireContext) kiroRequestUser {
		return kiroRequestUser{Content: content, ModelID: model, Origin: id.Origin, Context: context}
	}
	if len(ctx.Tools) == 0 {
		if len(schemas) != 0 {
			return nil, errKiroRequestEnvelope
		}
		projection, err := projectKiroTextConversation(ctx, id.ModelID, false, limits.Messages, limits.PayloadBytes)
		if err != nil {
			return nil, errKiroRequestEnvelope
		}
		request.ConversationState.CurrentMessage.User = user(projection.Current.Content, projection.Current.ModelID, nil)
		for _, entry := range projection.History {
			if entry.User != nil {
				u := user(entry.User.Content, entry.User.ModelID, nil)
				request.ConversationState.History = append(request.ConversationState.History, kiroRequestEntry{User: &u})
			} else {
				request.ConversationState.History = append(request.ConversationState.History, kiroRequestEntry{Assistant: &kiroToolWireAssistant{Content: entry.Assistant.Content}})
			}
		}
	} else {
		if len(ctx.Tools) > limits.Tools {
			return nil, errKiroRequestEnvelope
		}
		projection, err := projectKiroToolConversationWithPolicy(ctx, id.ModelID, schemas, limits.Messages, limits.Tools, limits.PayloadBytes, policy)
		if err != nil {
			return nil, errKiroRequestEnvelope
		}
		request.ConversationState.CurrentMessage.User = user(projection.Current.Content, projection.Current.ModelID, projection.Current.Context)
		for _, entry := range projection.History {
			if entry.User != nil {
				u := user(entry.User.Content, entry.User.ModelID, entry.User.Context)
				request.ConversationState.History = append(request.ConversationState.History, kiroRequestEntry{User: &u})
			} else {
				request.ConversationState.History = append(request.ConversationState.History, kiroRequestEntry{Assistant: entry.Assistant})
			}
		}
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > limits.WireBytes {
		return nil, errKiroRequestEnvelope
	}
	return encoded, nil
}
