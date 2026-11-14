package pigo

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type sseByteReader struct{ io.Reader }

func (r sseByteReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}

func TestSSELineEndingVariants(t *testing.T) {
	for _, eol := range []string{"\n", "\r\n", "\r"} {
		for _, bytewise := range []bool{false, true} {
			t.Run(strings.NewReplacer("\r", "CR", "\n", "LF").Replace(eol)+map[bool]string{true: "/bytewise", false: "/buffered"}[bytewise], func(t *testing.T) {
				input := strings.Join([]string{": comment", "event: fixture", `data: {"text":"a\rb","number":9007199254740993}`, "", "", "data: second", "data: line", "", "data: eof"}, eol)
				var reader io.Reader = strings.NewReader(input)
				if bytewise {
					reader = sseByteReader{reader}
				}
				var got []string
				err := readSSEStream(reader, func(event, data string) (bool, error) { got = append(got, event+"|"+data); return false, nil })
				want := []string{`fixture|{"text":"a\rb","number":9007199254740993}`, "|second\nline", "|eof"}
				if err != nil || len(got) != len(want) {
					t.Fatalf("events=%q error=%v", got, err)
				}
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("events=%q", got)
					}
				}
			})
		}
	}
}

func TestSSECRDispatchBeforeNextByte(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	delivered := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- readSSEStream(reader, func(_ string, data string) (bool, error) { delivered <- data; return true, nil })
	}()
	writeDone := make(chan error, 1)
	go func() { _, err := io.WriteString(writer, "data: ready\r\r"); writeDone <- err }()
	select {
	case data := <-delivered:
		if data != "ready" {
			t.Fatal(data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CR event waited for next byte")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
}

func TestSSECRReaderFailureRetainsError(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	sentinel := errors.New("fixture cancellation")
	done := make(chan error, 1)
	go func() {
		done <- readSSEStream(reader, func(string, string) (bool, error) { t.Error("unfinished event dispatched on error"); return false, nil })
	}()
	go func() { io.WriteString(writer, "data: unfinished\r"); writer.CloseWithError(sentinel) }()
	if err := <-done; !errors.Is(err, sentinel) {
		t.Fatalf("reader cause lost: %v", err)
	}
}

func TestSSELineEndingBudgets(t *testing.T) {
	for _, eol := range []string{"\n", "\r\n", "\r"} {
		for _, size := range []int{64, 65} {
			// Each empty data line consumes one budget byte even with CR framing.
			input := strings.Repeat("data:"+eol, size) + eol
			calls := 0
			err := readSSEStreamLimit(sseByteReader{strings.NewReader(input)}, func(string, string) (bool, error) { calls++; return false, nil }, 64)
			if size == 64 {
				if err != nil || calls != 1 {
					t.Fatalf("within event cap: size=%d err=%v calls=%d", size, err, calls)
				}
			} else {
				if !errors.Is(err, ErrSSEEventTooLarge) || calls != 0 {
					t.Fatalf("event cap not enforced: %v calls=%d", err, calls)
				}
			}
		}
		err := readSSEStreamLimit(strings.NewReader("data: "+strings.Repeat("x", 80)+eol+eol), func(string, string) (bool, error) { t.Error("oversized line dispatched"); return false, nil }, 64)
		if !errors.Is(err, ErrSSEEventTooLarge) {
			t.Fatalf("line cap not enforced: %v", err)
		}
	}
}

func TestProviderSSECRActualHTTP(t *testing.T) {
	for _, api := range []API{"openai-completions", "openai-responses", "google-generative-ai"} {
		t.Run(string(api), func(t *testing.T) {
			payload := `{"choices":[{"delta":{"content":"answer"},"finish_reason":"stop"}]}`
			provider := Provider("openai")
			if api == "openai-responses" {
				payload = `{"type":"response.completed","response":{"id":"r","status":"completed","output":[]}}`
			}
			if api == "google-generative-ai" {
				provider = "google"
				payload = `{"candidates":[{"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}]}`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, ": comment\rdata: %s\r\r", payload)
			}))
			defer server.Close()
			result := Complete(Model{ID: "fixture", Provider: provider, API: api, BaseURL: server.URL}, Context{}, ProviderStreamOptions{APIKey: "fixture"})
			if result.StopReason != StopReasonStop {
				t.Fatalf("result=%+v", result)
			}
			if api != "openai-responses" && (len(result.Content) != 1 || result.Content[0].(TextContent).Text != "answer") {
				t.Fatalf("lost text: %+v", result)
			}
		})
	}
}

func TestProviderSSECRCancellation(t *testing.T) {
	closed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(closed)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": waiting\r\r")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	result := Complete(Model{ID: "fixture", Provider: "openai", API: "openai-responses", BaseURL: server.URL}, Context{}, ProviderStreamOptions{APIKey: "fixture", TimeoutMs: 50})
	if result.StopReason != StopReasonAborted {
		t.Fatalf("result=%+v", result)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("request survived cancellation")
	}
}

func TestSSEMixedLineEndingsAndTerminalDelimiter(t *testing.T) {
	for _, tail := range []string{"", "\r", "\r\n", "\n"} {
		var got []string
		input := "data: one\r\n\rdata: two\n\ndata: three" + tail
		err := readSSEStream(sseByteReader{strings.NewReader(input)}, func(_ string, data string) (bool, error) { got = append(got, data); return false, nil })
		if err != nil || strings.Join(got, "|") != "one|two|three" {
			t.Fatalf("tail=%q events=%q err=%v", tail, got, err)
		}
	}
}
