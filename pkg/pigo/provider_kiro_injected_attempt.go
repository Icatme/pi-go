package pigo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

var errKiroInjectedAttempt = errors.New("kiro: injected attempt failed")

type kiroHTTPResponseLimits struct {
	WireBytes int64
	Stream    kiroOfflineLimits
}

// runKiroInjectedAttempt calls ONLY the explicitly supplied function, once.
// There is no default network client, redirect follow, retry or token refresh.
// The executor is trusted to honor request context, not follow redirects itself,
// and return a context-aware body. We cannot preempt a blocked custom function,
// Read or Close without its cooperation; no background goroutines are spawned.
// Inputs and executor must not be mutated concurrently. No request is logged.
func runKiroInjectedAttempt(ctx context.Context, conversation Context, schemas map[string]json.RawMessage, options kiroHTTPRequestOptions, kind kiroCredentialKind, token string, roundTrip func(*http.Request) (*http.Response, error), limits kiroHTTPResponseLimits) (result kiroOfflineResponse, resultErr error) {
	zero := kiroOfflineResponse{}
	s := limits.Stream
	// Fixed ceilings bound admitted work, not process heap or wall-clock time.
	if roundTrip == nil || limits.WireBytes < 16 || limits.WireBytes > 16<<20 || s.MessageBytes < 16 || s.MessageBytes > 1<<20 || s.TotalPayloadBytes < 2 || s.TotalPayloadBytes > 16<<20 || s.TextBytes < 1 || s.TextBytes > 4<<20 || s.Events < 1 || s.Events > 65536 || s.Tools < 1 || s.Tools > 1024 || s.ArgumentBytes < 2 || s.ArgumentBytes > 1<<20 {
		return zero, errKiroInjectedAttempt
	}
	request, err := newKiroOfflineRequestWithToken(ctx, conversation, schemas, options, kind, token)
	if err != nil {
		return zero, errKiroInjectedAttempt
	}
	defer request.Body.Close()
	if ctx.Err() != nil {
		return zero, errKiroInjectedAttempt
	}
	response, transportErr := roundTrip(request)
	if response != nil && response.Body != nil {
		defer func() {
			closeErr := response.Body.Close()
			if closeErr != nil || ctx.Err() != nil {
				result, resultErr = zero, errKiroInjectedAttempt
			}
		}()
	}
	// Never expose transport error text: it may contain headers/URLs/secrets.
	if transportErr != nil || ctx.Err() != nil || response == nil || response.Body == nil || response.StatusCode != http.StatusOK || response.Uncompressed || response.ContentLength < -1 || response.ContentLength > limits.WireBytes {
		return zero, errKiroInjectedAttempt
	}
	contentType, encoding := "", ""
	typeSeen, encodingSeen := false, false
	for name, values := range response.Header {
		if strings.EqualFold(name, "Content-Type") {
			if typeSeen || len(values) != 1 || len(values[0]) > 256 {
				return zero, errKiroInjectedAttempt
			}
			typeSeen, contentType = true, values[0]
		}
		if strings.EqualFold(name, "Content-Encoding") {
			if encodingSeen || len(values) != 1 {
				return zero, errKiroInjectedAttempt
			}
			encodingSeen, encoding = true, values[0]
		}
	}
	if !typeSeen || (encodingSeen && encoding != "identity") {
		return zero, errKiroInjectedAttempt
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "application/vnd.amazon.eventstream" {
		return zero, errKiroInjectedAttempt
	}
	// One extra byte distinguishes true EOF at the exact budget from a limit-
	// generated EOF. An exhausted allowance must NEVER certify completion.
	reader := &io.LimitedReader{R: response.Body, N: limits.WireBytes + 1}
	result, resultErr = collectKiroOfflineResponse(ctx, reader, limits.Stream)
	if resultErr != nil || reader.N == 0 || (response.ContentLength >= 0 && response.ContentLength != limits.WireBytes+1-reader.N) || ctx.Err() != nil {
		return zero, errKiroInjectedAttempt
	}
	return result, nil
}
