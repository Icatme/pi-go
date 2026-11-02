package pigo

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

var errKiroCatalogAttempt = errors.New("kiro: injected catalog attempt failed")

type kiroCatalogAttemptOptions struct {
	Request             kiroCatalogRequestOptions
	MaxBytes, MaxModels int
	Clock               func(context.Context) (time.Time, error)
}

// runKiroInjectedCatalogAttempt invokes only the explicitly supplied executor,
// once, without a default client, redirect follow, retries, profile probes, cache
// or registration. Source/executor/Read/Close/clock are trusted and must cooperate
// with context. No arbitrary URL is accepted. Do not log request/response data.
func runKiroInjectedCatalogAttempt(ctx context.Context, options kiroCatalogAttemptOptions, source kiroCredentialSource, roundTrip func(*http.Request) (*http.Response, error)) (*kiroScopedModelCatalog, error) {
	if roundTrip == nil || options.Clock == nil || options.MaxBytes < 2 || options.MaxBytes > 1<<20 || options.MaxModels < 1 || options.MaxModels > 1024 {
		return nil, errKiroCatalogAttempt
	}
	request, err := newKiroOfflineCatalogRequest(ctx, options.Request, source)
	if err != nil {
		return nil, errKiroCatalogAttempt
	}
	response, transportErr := roundTrip(request)
	payload, err := readKiroCatalogHTTPResponse(ctx, response, transportErr, options.MaxBytes)
	if err != nil || ctx.Err() != nil {
		return nil, errKiroCatalogAttempt
	}
	// Clock and observation construction occur only after body Close succeeds.
	stamp, err := options.Clock(ctx)
	if err != nil || ctx.Err() != nil {
		return nil, errKiroCatalogAttempt
	}
	catalog, err := newKiroScopedModelCatalog(payload, options.Request.Scope, stamp, options.MaxBytes, options.MaxModels)
	if err != nil || ctx.Err() != nil {
		return nil, errKiroCatalogAttempt
	}
	return catalog, nil
}

func readKiroCatalogHTTPResponse(ctx context.Context, response *http.Response, transportErr error, maxBytes int) (payload []byte, resultErr error) {
	if response != nil && response.Body != nil {
		defer func() {
			closeErr := response.Body.Close()
			if closeErr != nil || ctx == nil || ctx.Err() != nil {
				payload, resultErr = nil, errKiroCatalogAttempt
			}
		}()
	}
	if ctx == nil || ctx.Err() != nil || maxBytes < 2 || maxBytes > 1<<20 || transportErr != nil || response == nil || response.Body == nil || response.StatusCode != http.StatusOK || response.Uncompressed || response.ContentLength < -1 || response.ContentLength > int64(maxBytes) {
		return nil, errKiroCatalogAttempt
	}
	contentType, encoding := "", ""
	seenType, seenEncoding := false, false
	for name, values := range response.Header {
		if strings.EqualFold(name, "Content-Type") {
			if seenType || len(values) != 1 || len(values[0]) > 256 {
				return nil, errKiroCatalogAttempt
			}
			seenType, contentType = true, values[0]
		}
		if strings.EqualFold(name, "Content-Encoding") {
			if seenEncoding || len(values) != 1 {
				return nil, errKiroCatalogAttempt
			}
			seenEncoding, encoding = true, values[0]
		}
	}
	if !seenType || (seenEncoding && encoding != "identity") {
		return nil, errKiroCatalogAttempt
	}
	media, params, err := mime.ParseMediaType(contentType)
	if err != nil || media != "application/json" {
		return nil, errKiroCatalogAttempt
	}
	for name, value := range params {
		if name != "charset" || !strings.EqualFold(value, "utf-8") {
			return nil, errKiroCatalogAttempt
		}
	}
	guarded := &kiroOfflineContextReader{ctx: ctx, reader: response.Body}
	limited := &io.LimitedReader{R: guarded, N: int64(maxBytes) + 1}
	payload, err = io.ReadAll(limited)
	if err != nil || guarded.failure != nil || limited.N == 0 || ctx.Err() != nil || (response.ContentLength >= 0 && response.ContentLength != int64(len(payload))) {
		return nil, errKiroCatalogAttempt
	}
	return payload, nil
}
