package pigo

import (
	"encoding/json"
	"net/http"
	"strings"
)

// 1308 is a periodic usage cap, unlike transient 1302/1305 throttling.
// Only explicitly identified providers qualify; model IDs and endpoint names
// do not opt other providers into this policy or change request routing.
// https://docs.bigmodel.cn/cn/api/api-code
// https://docs.z.ai/api-reference/api-code
func isZaiPeriodicQuota(model Model, status int, body []byte) bool {
	switch strings.ToLower(strings.TrimSpace(string(model.Provider))) {
	case "zai", "zhipu", "bigmodel":
	default:
		return false
	}
	if status != http.StatusTooManyRequests {
		return false
	}
	type quotaError struct {
		Code json.RawMessage `json:"code"`
	}
	var envelope struct {
		Code  json.RawMessage `json:"code"`
		Error *quotaError     `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return false
	}
	code := envelope.Code
	if envelope.Error != nil {
		code = envelope.Error.Code
	}
	return string(code) == `"1308"` || string(code) == "1308"
}
