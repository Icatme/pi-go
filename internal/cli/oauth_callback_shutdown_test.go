package cli

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestCodexCallbackShutdownSpareConnection(t *testing.T) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	addr := l.Addr().String()
	l.Close()
	old := openAICodexCallbackAddress
	openAICodexCallbackAddress = addr
	defer func() { openAICodexCallbackAddress = old }()
	s, e := startOpenAICodexCallbackServer("fixture")
	if e != nil {
		t.Fatal(e)
	}
	defer s.server.Close()
	spare, e := net.Dial("tcp", addr)
	if e != nil {
		t.Fatal(e)
	}
	defer spare.Close()
	resp, e := http.Get("http://" + addr + "/auth/callback?state=fixture&code=synthetic")
	if e != nil {
		t.Fatal(e)
	}
	body, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil || resp.StatusCode != 200 || string(body) != "<html><body>OpenAI authentication completed. You can close this window.</body></html>" {
		t.Fatal("incomplete success page")
	}
	code, e := s.WaitForCode(context.Background(), time.Second)
	if e != nil || code != "synthetic" {
		t.Fatal(code, e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e = s.WaitForCode(ctx, time.Second)
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	start := time.Now()
	s.Close()
	elapsed := time.Since(start)
	spare.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	var b [1]byte
	_, e = spare.Read(b[:])
	t.Logf("Close elapsed=%v spare read=%v", elapsed, e)
	if elapsed > 3*time.Second {
		t.Fatal("unbounded callback shutdown")
	}
	if ne, ok := e.(net.Error); ok && ne.Timeout() {
		t.Fatal("callback Close returned with spare TCP still open")
	}
	if e == nil {
		t.Fatal("spare still readable")
	}
	start = time.Now()
	s.Close()
	if time.Since(start) > time.Second {
		t.Fatal("repeated Close blocked")
	}
}

func TestCodexCallbackShutdownIncompleteHeader(t *testing.T) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	accepted := make(chan struct{})
	s := &openAICodexCallbackServer{server: &http.Server{ConnState: func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			close(accepted)
		}
	}, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("incomplete request dispatched") })}}
	defer s.server.Close()
	go s.server.Serve(l)
	conn, e := net.Dial("tcp", l.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.Write([]byte("GET / HTTP/1.1\r\nHost: fixture\r\n"))
	select {
	case <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("socket not accepted")
	}
	start := time.Now()
	s.Close()
	if time.Since(start) > 3*time.Second {
		t.Fatal("Close exceeded bounded wait")
	}
	conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	var b [1]byte
	_, e = conn.Read(b[:])
	if e == nil {
		t.Fatal("unexpected response")
	}
	if ne, ok := e.(net.Error); ok && ne.Timeout() {
		t.Fatal("incomplete header connection survived Close")
	}
}

func TestCodexCallbackShutdownPreservesActiveResponse(t *testing.T) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	s := &openAICodexCallbackServer{server: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		io.WriteString(w, "<html><body>complete fixture</body></html>")
	})}}
	defer s.server.Close()
	go s.server.Serve(l)
	result := make(chan error, 1)
	go func() {
		resp, e := http.Get("http://" + l.Addr().String())
		if e != nil {
			result <- e
			return
		}
		defer resp.Body.Close()
		b, e := io.ReadAll(resp.Body)
		if e == nil && string(b) != "<html><body>complete fixture</body></html>" {
			e = errors.New("incomplete body")
		}
		result <- e
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("handler not entered")
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
		t.Error("Close did not wait for active response")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case e := <-result:
		if e != nil {
			t.Error(e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("response not delivered")
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("graceful shutdown did not finish")
	}
}

func TestCodexCallbackShutdownErrorPage(t *testing.T) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	addr := l.Addr().String()
	l.Close()
	old := openAICodexCallbackAddress
	openAICodexCallbackAddress = addr
	defer func() { openAICodexCallbackAddress = old }()
	s, e := startOpenAICodexCallbackServer("fixture")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	resp, e := http.Get("http://" + addr + "/auth/callback?state=wrong&code=synthetic")
	if e != nil {
		t.Fatal(e)
	}
	b, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil || resp.StatusCode != 400 || string(b) != "<html><body>State mismatch.</body></html>" {
		t.Fatalf("error page status=%d body=%q err=%v", resp.StatusCode, b, e)
	}
	start := time.Now()
	s.Close()
	if time.Since(start) > time.Second {
		t.Fatal("normal shutdown unexpectedly waited full budget")
	}
}
