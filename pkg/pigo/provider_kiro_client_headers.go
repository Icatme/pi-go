package pigo

import (
	"errors"
	"strings"
)

var errKiroClientHeaders = errors.New("kiro: invalid offline client identity")

// kiroCommunityCLIUserAgent is an explicit compatibility simulation of the
// pinned community plugin's application-layer UA. It does not identify a real
// AWS SDK/CLI installation, implement TLS fingerprinting, or grant service use.
// Callers choose whether to use it; no other function selects it implicitly.
func kiroCommunityCLIUserAgent(instanceID string) (string, error) {
	if len(instanceID) != 32 || !kiroLowerHex(instanceID) {
		return "", errKiroClientHeaders
	}
	return "aws-sdk-rust/1.0.0 ua/2.1 os/other lang/rust api/codewhispererstreaming#1.28.3 m/E app/AmazonQ-For-CLI md/appVersion-1.28.3-" + instanceID, nil
}

func kiroLowerHex(value string) bool {
	for _, c := range value {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// buildKiroOfflineClientHeaders creates an owned application-header map only.
// userAgent and invocationID are explicitly injected; no randomness, runtime
// fingerprint, machine identifiers, environment variables or token discovery.
// Authentication headers are deliberately absent. The request-attempt marker
// mirrors the community wire format, not an implemented retry policy.
func buildKiroOfflineClientHeaders(userAgent, invocationID string) (map[string]string, error) {
	if len(userAgent) == 0 || len(userAgent) > 512 || strings.TrimSpace(userAgent) != userAgent {
		return nil, errKiroClientHeaders
	}
	for _, c := range userAgent {
		if c < 0x20 || c > 0x7e {
			return nil, errKiroClientHeaders
		}
	}
	if len(invocationID) != 36 {
		return nil, errKiroClientHeaders
	}
	for i, c := range invocationID {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return nil, errKiroClientHeaders
			}
		} else if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return nil, errKiroClientHeaders
		}
	}
	return map[string]string{
		"Content-Type":                "application/json",
		"Accept":                      "application/vnd.amazon.eventstream",
		"x-amzn-codewhisperer-optout": "true",
		"amz-sdk-invocation-id":       invocationID,
		"amz-sdk-request":             "attempt=1; max=1",
		"x-amzn-kiro-agent-mode":      "vibe",
		"x-amz-user-agent":            userAgent,
		"user-agent":                  userAgent,
	}, nil
}
