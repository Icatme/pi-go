package pigo

import (
	"encoding/json"
	"net/http"
	"strings"
)

// DashScope reuses insufficient_quota for token-rate limits. Do not relax the
// generic OpenAI policy: only explicitly identified DashScope-family models,
// HTTP 429, and the documented code/message pairs qualify. Unknown wording
// remains subject to the existing policy, including real billing exhaustion.
// https://www.alibabacloud.com/help/en/model-studio/error-code
func isDashScopeTokenThrottle(model Model, status int, body []byte) bool {
	switch strings.ToLower(string(model.Provider)) {
	case "dashscope", "bailian", "alibaba":
	default:
		return false
	}
	if status != http.StatusTooManyRequests {
		return false
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return false
	}
	code := strings.ToLower(envelope.Error.Code)
	if code != "insufficient_quota" && code != "throttling.allocationquota" {
		return false
	}
	kind := strings.ToLower(envelope.Error.Type)
	if kind != "" && kind != "insufficient_quota" && kind != "throttling.allocationquota" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(envelope.Error.Message)) {
	case "allocated quota exceeded, please increase your quota limit.", "you exceeded your current quota, please check your plan and billing details.":
		return true
	default:
		return false
	}
}

func shouldRetryCompletionsModelResponse(model Model, status int, body []byte, message string) bool {
	if isZaiPeriodicQuota(model, status, body) {
		return false
	}
	switch strings.ToLower(string(model.Provider)) {
	case "dashscope", "bailian", "alibaba":
		var envelope struct {
			Error struct {
				Code string `json:"code"`
				Type string `json:"type"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil {
			for _, value := range []string{envelope.Error.Code, envelope.Error.Type} {
				switch strings.ToLower(value) {
				case "prepaidbilloverdue", "postpaidbilloverdue", "budgetlimitexceeded", "commoditynotpurchased":
					return false
				}
			}
		}
	}
	return isDashScopeTokenThrottle(model, status, body) || shouldRetryOpenAIProviderResponse(status, body, message)
}
