package pigo

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPStreamClientPostStreamSendsHeadersAndBody(t *testing.T) {
	var requestBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-test") != "value" {
			t.Fatalf("expected custom header, got %q", r.Header.Get("x-test"))
		}
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("expected request body to be readable: %v", err)
		}
		requestBody = string(bodyBytes)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: hello\n\n"))
	}))
	defer server.Close()

	client := HTTPStreamClient{HTTPClient: server.Client()}
	var events []string
	err := client.PostStream(context.Background(), server.URL, map[string]string{"x-test": "value"}, []byte(`{"hello":"world"}`), func(eventName, data string) (bool, error) {
		events = append(events, eventName+":"+data)
		return true, nil
	})
	if err != nil {
		t.Fatalf("expected PostStream to succeed, got %v", err)
	}
	if requestBody != `{"hello":"world"}` {
		t.Fatalf("expected request body to match fixture, got %q", requestBody)
	}
	if len(events) != 1 || events[0] != "message:hello" {
		t.Fatalf("expected a single SSE event, got %+v", events)
	}
}

func TestHTTPStreamClientPostStreamRetriesTransientFailure(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attempts.Add(1)
		if attempt == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte("data: done\n\n"))
	}))
	defer server.Close()

	client := HTTPStreamClient{
		HTTPClient:     server.Client(),
		MaxRetries:     1,
		BaseRetryDelay: time.Millisecond,
	}
	err := client.PostStream(context.Background(), server.URL, nil, []byte(`{}`), func(_ string, data string) (bool, error) {
		return data == "done", nil
	})
	if err != nil {
		t.Fatalf("expected retry flow to succeed, got %v", err)
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected one retry before success, got %d attempts", attempts.Load())
	}
}

func TestHTTPStreamClientPostStreamParsesMultiLineSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Join([]string{
			"event: update",
			"data: line1",
			"data: line2",
			"",
		}, "\n")))
	}))
	defer server.Close()

	client := HTTPStreamClient{HTTPClient: server.Client()}
	var payload string
	err := client.PostStream(context.Background(), server.URL, nil, []byte(`{}`), func(eventName, data string) (bool, error) {
		payload = eventName + ":" + data
		return true, nil
	})
	if err != nil {
		t.Fatalf("expected PostStream to succeed, got %v", err)
	}
	if payload != "update:line1\nline2" {
		t.Fatalf("expected multiline payload to be preserved, got %q", payload)
	}
}
