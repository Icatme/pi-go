package pigo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const openAICodexWebSocketBetaHeader = "responses_websockets=2026-02-06"

var (
	openAICodexRetryCount            = 3
	openAICodexBaseRetryDelay        = time.Second
	openAICodexWebSocketCacheTTL     = 5 * time.Minute
	openAICodexWebSocketCacheMu      sync.Mutex
	openAICodexWebSocketSessionCache = map[string]*cachedOpenAICodexWebSocketConnection{}
)

type cachedOpenAICodexWebSocketConnection struct {
	conn      *websocket.Conn
	busy      bool
	closed    bool
	idleTimer *time.Timer
}

func streamOpenAICodexWithTransport(
	model Model,
	options ProviderStreamOptions,
	bodyBytes []byte,
	apiKey string,
	accountID string,
	response *AssistantMessage,
	stream *AssistantMessageEventStream,
) error {
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	requestContext := options.RequestContext
	if requestContext == nil {
		requestContext = context.Background()
	}

	transport := options.Transport
	if transport == "" {
		transport = TransportSSE
	}

	if transport != TransportSSE {
		websocketStarted := false
		err := streamOpenAICodexWebSocket(model, options, bodyBytes, apiKey, accountID, response, stream, func() {
			websocketStarted = true
		})
		if err == nil {
			return nil
		}
		if transport == TransportWebSocket || websocketStarted {
			return err
		}
	}

	return streamOpenAICodexSSE(model, requestContext, httpClient, options, bodyBytes, apiKey, accountID, response, stream)
}

func streamOpenAICodexSSE(
	model Model,
	requestContext context.Context,
	httpClient *http.Client,
	options ProviderStreamOptions,
	bodyBytes []byte,
	apiKey string,
	accountID string,
	response *AssistantMessage,
	stream *AssistantMessageEventStream,
) error {
	for attempt := 0; attempt <= openAICodexRetryCount; attempt++ {
		request, err := http.NewRequestWithContext(
			requestContext,
			http.MethodPost,
			resolveOpenAICodexURL(model.BaseURL),
			bytes.NewReader(bodyBytes),
		)
		if err != nil {
			return err
		}

		request.Header.Set("content-type", "application/json")
		request.Header.Set("accept", "text/event-stream")
		request.Header.Set("authorization", "Bearer "+apiKey)
		request.Header.Set("chatgpt-account-id", accountID)
		request.Header.Set("originator", "pi")
		request.Header.Set("openai-beta", "responses=experimental")
		requestID := options.SessionID
		if strings.TrimSpace(requestID) == "" {
			requestID = createOpenAICodexRequestID()
		}
		request.Header.Set("x-client-request-id", requestID)
		if options.SessionID != "" {
			request.Header.Set("conversation_id", options.SessionID)
			request.Header.Set("session_id", options.SessionID)
		}
		for key, value := range options.Headers {
			request.Header.Set(key, value)
		}

		httpResponse, err := httpClient.Do(request)
		if err != nil {
			if shouldRetryOpenAICodexRequest(0, err.Error()) && attempt < openAICodexRetryCount {
				if waitErr := waitOpenAICodexRetryDelay(requestContext, options.MaxRetryDelay, attempt); waitErr != nil {
					return waitErr
				}
				continue
			}
			return err
		}

		if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
			body, _ := io.ReadAll(httpResponse.Body)
			_ = httpResponse.Body.Close()
			message := parseOpenAICodexError(body, httpResponse.Status)
			if shouldRetryOpenAICodexRequest(httpResponse.StatusCode, message) && attempt < openAICodexRetryCount {
				if waitErr := waitOpenAICodexRetryDelay(requestContext, options.MaxRetryDelay, attempt); waitErr != nil {
					return waitErr
				}
				continue
			}
			return errors.New(message)
		}

		stream.push(AssistantMessageEvent{
			Type:    AssistantMessageEventStart,
			Partial: *response,
		})

		state := openAICodexStreamingState{
			CurrentTextIndex:     -1,
			CurrentThinkingIndex: -1,
			CurrentToolIndex:     -1,
			FinalizedItemKeys:    map[string]bool{},
		}
		terminalSeen := false
		err = readSSEStream(httpResponse.Body, func(_ string, data string) (bool, error) {
			done, err := processOpenAICodexStreamEvent(data, model, response, stream, &state, options.ServiceTier)
			if done {
				terminalSeen = true
				return true, nil
			}
			return false, err
		})
		_ = httpResponse.Body.Close()
		if err != nil {
			return err
		}
		if !terminalSeen {
			return fmt.Errorf("missing terminal sse event")
		}
		return nil
	}

	return fmt.Errorf("codex request failed after retries")
}

func streamOpenAICodexWebSocket(
	model Model,
	options ProviderStreamOptions,
	bodyBytes []byte,
	apiKey string,
	accountID string,
	response *AssistantMessage,
	stream *AssistantMessageEventStream,
	onStart func(),
) error {
	requestID := options.SessionID
	if strings.TrimSpace(requestID) == "" {
		requestID = createOpenAICodexRequestID()
	}

	headers := http.Header{}
	headers.Set("authorization", "Bearer "+apiKey)
	headers.Set("chatgpt-account-id", accountID)
	headers.Set("originator", "pi")
	headers.Set("openai-beta", openAICodexWebSocketBetaHeader)
	headers.Set("x-client-request-id", requestID)
	headers.Set("session_id", requestID)
	for key, value := range options.Headers {
		headers.Set(key, value)
	}

	dialer := websocket.Dialer{}
	requestContext := options.RequestContext
	if requestContext == nil {
		requestContext = context.Background()
	}

	acquired, err := acquireOpenAICodexWebSocket(
		dialer,
		resolveOpenAICodexWebSocketURL(model.BaseURL),
		headers,
		options.SessionID,
		requestContext,
	)
	if err != nil {
		return err
	}
	keepConnection := true
	defer func() {
		acquired.release(keepConnection)
	}()

	payload := map[string]any{"type": "response.create"}
	if err := jsonUnmarshalIntoMap(bodyBytes, payload); err != nil {
		keepConnection = false
		return err
	}
	if err := acquired.entry.conn.WriteJSON(payload); err != nil {
		markOpenAICodexWebSocketClosed(acquired.entry)
		keepConnection = false
		return err
	}

	onStart()
	stream.push(AssistantMessageEvent{
		Type:    AssistantMessageEventStart,
		Partial: *response,
	})

	state := openAICodexStreamingState{
		CurrentTextIndex:     -1,
		CurrentThinkingIndex: -1,
		CurrentToolIndex:     -1,
		FinalizedItemKeys:    map[string]bool{},
	}
	for {
		if err := requestContext.Err(); err != nil {
			keepConnection = false
			return err
		}

		_, message, err := acquired.entry.conn.ReadMessage()
		if err != nil {
			markOpenAICodexWebSocketClosed(acquired.entry)
			keepConnection = false
			return err
		}

		done, err := processOpenAICodexStreamEvent(string(message), model, response, stream, &state, options.ServiceTier)
		if err != nil {
			keepConnection = false
			return err
		}
		if done {
			return nil
		}
	}
}

type acquiredOpenAICodexWebSocket struct {
	entry   *cachedOpenAICodexWebSocketConnection
	release func(keep bool)
}

func acquireOpenAICodexWebSocket(
	dialer websocket.Dialer,
	url string,
	headers http.Header,
	sessionID string,
	requestContext context.Context,
) (*acquiredOpenAICodexWebSocket, error) {
	if strings.TrimSpace(sessionID) == "" {
		entry, err := connectOpenAICodexWebSocket(dialer, url, headers, requestContext)
		if err != nil {
			return nil, err
		}
		return &acquiredOpenAICodexWebSocket{
			entry: entry,
			release: func(_ bool) {
				closeOpenAICodexWebSocket(entry)
			},
		}, nil
	}

	openAICodexWebSocketCacheMu.Lock()
	if cached := openAICodexWebSocketSessionCache[sessionID]; cached != nil {
		stopOpenAICodexWebSocketTimerLocked(cached)
		if !cached.busy && !cached.closed {
			cached.busy = true
			openAICodexWebSocketCacheMu.Unlock()
			return &acquiredOpenAICodexWebSocket{
				entry: cached,
				release: func(keep bool) {
					releaseOpenAICodexSessionWebSocket(sessionID, cached, keep)
				},
			}, nil
		}
		if cached.closed {
			delete(openAICodexWebSocketSessionCache, sessionID)
		}
	}
	openAICodexWebSocketCacheMu.Unlock()

	entry, err := connectOpenAICodexWebSocket(dialer, url, headers, requestContext)
	if err != nil {
		return nil, err
	}
	entry.busy = true

	openAICodexWebSocketCacheMu.Lock()
	if existing := openAICodexWebSocketSessionCache[sessionID]; existing == nil || existing.closed {
		openAICodexWebSocketSessionCache[sessionID] = entry
		openAICodexWebSocketCacheMu.Unlock()
		return &acquiredOpenAICodexWebSocket{
			entry: entry,
			release: func(keep bool) {
				releaseOpenAICodexSessionWebSocket(sessionID, entry, keep)
			},
		}, nil
	}
	openAICodexWebSocketCacheMu.Unlock()

	return &acquiredOpenAICodexWebSocket{
		entry: entry,
		release: func(_ bool) {
			closeOpenAICodexWebSocket(entry)
		},
	}, nil
}

func connectOpenAICodexWebSocket(
	dialer websocket.Dialer,
	url string,
	headers http.Header,
	requestContext context.Context,
) (*cachedOpenAICodexWebSocketConnection, error) {
	conn, _, err := dialer.DialContext(requestContext, url, headers)
	if err != nil {
		return nil, err
	}
	return &cachedOpenAICodexWebSocketConnection{conn: conn}, nil
}

func releaseOpenAICodexSessionWebSocket(
	sessionID string,
	entry *cachedOpenAICodexWebSocketConnection,
	keep bool,
) {
	openAICodexWebSocketCacheMu.Lock()
	current := openAICodexWebSocketSessionCache[sessionID]
	if current != entry {
		openAICodexWebSocketCacheMu.Unlock()
		closeOpenAICodexWebSocket(entry)
		return
	}
	if !keep || entry.closed {
		delete(openAICodexWebSocketSessionCache, sessionID)
		conn := detachOpenAICodexWebSocketConnLocked(entry)
		openAICodexWebSocketCacheMu.Unlock()
		closeDetachedOpenAICodexWebSocket(conn)
		return
	}
	entry.busy = false
	scheduleOpenAICodexSessionWebSocketExpiryLocked(sessionID, entry)
	openAICodexWebSocketCacheMu.Unlock()
}

func scheduleOpenAICodexSessionWebSocketExpiryLocked(
	sessionID string,
	entry *cachedOpenAICodexWebSocketConnection,
) {
	stopOpenAICodexWebSocketTimerLocked(entry)
	entry.idleTimer = time.AfterFunc(openAICodexWebSocketCacheTTL, func() {
		openAICodexWebSocketCacheMu.Lock()
		current := openAICodexWebSocketSessionCache[sessionID]
		if current != entry || entry.busy {
			openAICodexWebSocketCacheMu.Unlock()
			return
		}
		delete(openAICodexWebSocketSessionCache, sessionID)
		conn := detachOpenAICodexWebSocketConnLocked(entry)
		openAICodexWebSocketCacheMu.Unlock()
		closeDetachedOpenAICodexWebSocket(conn)
	})
}

func stopOpenAICodexWebSocketTimer(entry *cachedOpenAICodexWebSocketConnection) {
	openAICodexWebSocketCacheMu.Lock()
	defer openAICodexWebSocketCacheMu.Unlock()
	stopOpenAICodexWebSocketTimerLocked(entry)
}

func markOpenAICodexWebSocketClosed(entry *cachedOpenAICodexWebSocketConnection) {
	if entry == nil {
		return
	}
	openAICodexWebSocketCacheMu.Lock()
	defer openAICodexWebSocketCacheMu.Unlock()
	stopOpenAICodexWebSocketTimerLocked(entry)
	entry.closed = true
}

func closeOpenAICodexWebSocket(entry *cachedOpenAICodexWebSocketConnection) {
	closeDetachedOpenAICodexWebSocket(detachOpenAICodexWebSocketConn(entry))
}

func stopOpenAICodexWebSocketTimerLocked(entry *cachedOpenAICodexWebSocketConnection) {
	if entry == nil || entry.idleTimer == nil {
		return
	}
	entry.idleTimer.Stop()
	entry.idleTimer = nil
}

func detachOpenAICodexWebSocketConn(entry *cachedOpenAICodexWebSocketConnection) *websocket.Conn {
	openAICodexWebSocketCacheMu.Lock()
	defer openAICodexWebSocketCacheMu.Unlock()
	return detachOpenAICodexWebSocketConnLocked(entry)
}

func detachOpenAICodexWebSocketConnLocked(entry *cachedOpenAICodexWebSocketConnection) *websocket.Conn {
	if entry == nil {
		return nil
	}
	stopOpenAICodexWebSocketTimerLocked(entry)
	entry.closed = true
	conn := entry.conn
	entry.conn = nil
	return conn
}

func closeDetachedOpenAICodexWebSocket(conn *websocket.Conn) {
	if conn != nil {
		_ = conn.Close()
	}
}

func getOpenAICodexWebSocketCacheTTL() time.Duration {
	openAICodexWebSocketCacheMu.Lock()
	defer openAICodexWebSocketCacheMu.Unlock()
	return openAICodexWebSocketCacheTTL
}

func setOpenAICodexWebSocketCacheTTL(ttl time.Duration) {
	openAICodexWebSocketCacheMu.Lock()
	openAICodexWebSocketCacheTTL = ttl
	openAICodexWebSocketCacheMu.Unlock()
}

func resolveOpenAICodexWebSocketURL(baseURL string) string {
	resolved := resolveOpenAICodexURL(baseURL)
	parsed, err := url.Parse(resolved)
	if err != nil {
		return resolved
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	}
	return parsed.String()
}

func shouldRetryOpenAICodexRequest(status int, message string) bool {
	if status == http.StatusTooManyRequests ||
		status == http.StatusInternalServerError ||
		status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout {
		return true
	}

	lower := strings.ToLower(message)
	return strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "ratelimit") ||
		strings.Contains(lower, "overloaded") ||
		strings.Contains(lower, "service unavailable") ||
		strings.Contains(lower, "upstream connect") ||
		strings.Contains(lower, "connection refused")
}

func waitOpenAICodexRetryDelay(ctx context.Context, maxRetryDelayMS int, attempt int) error {
	delay := openAICodexBaseRetryDelay * time.Duration(1<<attempt)
	if maxRetryDelayMS > 0 {
		maxDelay := time.Duration(maxRetryDelayMS) * time.Millisecond
		if delay > maxDelay {
			delay = maxDelay
		}
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

func createOpenAICodexRequestID() string {
	return fmt.Sprintf("codex_%d", time.Now().UnixNano())
}

func jsonUnmarshalIntoMap(bodyBytes []byte, target map[string]any) error {
	var decoded map[string]any
	if err := json.Unmarshal(bodyBytes, &decoded); err != nil {
		return err
	}
	for key, value := range decoded {
		target[key] = value
	}
	return nil
}
