package pigo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"
)

// HTTPStreamClient sends SSE-based POST requests with shared retry behavior.
type HTTPStreamClient struct {
	HTTPClient     *http.Client
	MaxRetries     int
	BaseRetryDelay time.Duration
	MaxRetryDelay  time.Duration
}

type httpStreamRequest struct {
	Headers     map[string]string
	Body        []byte
	OnOpen      func(*http.Response) error
	OnEvent     func(eventName, data string) (bool, error)
	OnResponse  func(*http.Response)
	ShouldRetry func(status int, message string) bool
	ParseError  func(body []byte, status string) string
}

// PostStream performs a streaming POST request and dispatches SSE events.
func (c *HTTPStreamClient) PostStream(
	ctx context.Context,
	url string,
	headers map[string]string,
	body []byte,
	onEvent func(eventName, data string) (bool, error),
) error {
	return c.postStream(ctx, url, httpStreamRequest{
		Headers: headers,
		Body:    body,
		OnEvent: onEvent,
		ShouldRetry: func(status int, message string) bool {
			return shouldRetryOpenAIResponsesRequest(status, message)
		},
		ParseError: func(body []byte, status string) string {
			if len(body) == 0 {
				return status
			}
			return string(body)
		},
	})
}

func (c *HTTPStreamClient) postStream(ctx context.Context, url string, requestOptions httpStreamRequest) error {
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	maxRetries := c.MaxRetries
	baseRetryDelay := c.BaseRetryDelay
	if baseRetryDelay <= 0 {
		baseRetryDelay = time.Second
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(requestOptions.Body))
		if err != nil {
			return err
		}
		for key, value := range requestOptions.Headers {
			request.Header.Set(key, value)
		}

		httpResponse, err := httpClient.Do(request)
		if err != nil {
			if requestOptions.ShouldRetry != nil && requestOptions.ShouldRetry(0, err.Error()) && attempt < maxRetries {
				if waitErr := c.waitRetryDelay(ctx, attempt, baseRetryDelay); waitErr != nil {
					return waitErr
				}
				continue
			}
			return err
		}

		if requestOptions.OnResponse != nil {
			requestOptions.OnResponse(httpResponse)
		}

		if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
			body, _ := io.ReadAll(httpResponse.Body)
			_ = httpResponse.Body.Close()
			message := httpResponse.Status
			if requestOptions.ParseError != nil {
				message = requestOptions.ParseError(body, httpResponse.Status)
			}
			if requestOptions.ShouldRetry != nil && requestOptions.ShouldRetry(httpResponse.StatusCode, message) && attempt < maxRetries {
				if waitErr := c.waitRetryDelay(ctx, attempt, baseRetryDelay); waitErr != nil {
					return waitErr
				}
				continue
			}
			return errors.New(message)
		}

		if requestOptions.OnOpen != nil {
			if err := requestOptions.OnOpen(httpResponse); err != nil {
				_ = httpResponse.Body.Close()
				return err
			}
		}

		err = readSSEStream(httpResponse.Body, requestOptions.OnEvent)
		_ = httpResponse.Body.Close()
		if err != nil {
			return err
		}
		return nil
	}

	return errors.New("stream request failed after retries")
}

func (c *HTTPStreamClient) waitRetryDelay(ctx context.Context, attempt int, baseDelay time.Duration) error {
	delay := baseDelay * time.Duration(1<<attempt)
	if c.MaxRetryDelay > 0 && delay > c.MaxRetryDelay {
		delay = c.MaxRetryDelay
	}
	if delay <= 0 {
		return ctx.Err()
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
