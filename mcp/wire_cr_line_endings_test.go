package mcp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func TestUpstream11764CRLineEndings(t *testing.T) {
	for _, sep := range []string{"\n", "\r\n", "\r"} {
		t.Run(strings.ReplaceAll(strings.ReplaceAll(sep, "\r", "CR"), "\n", "LF"), func(t *testing.T) {
			o := NewObserver(WireLimits{})
			defer o.Close()
			token, slot, _ := o.begin("tools/call")
			defer o.finish(token)
			if err := o.observeFrame(wireRequest(1, token, "tools/call"), true); err != nil {
				t.Fatal(err)
			}
			event := "id: result" + sep + `data: {"jsonrpc":"2.0","id":1,"result":{"ok":true,"escaped":"a\r\nb"}}` + sep + sep
			src := io.NopCloser(strings.NewReader(event))
			b := &observedSSEBody{observer: o, source: src, reader: bufio.NewReader(src)}
			_, err := io.ReadAll(b)
			if err != nil || !bytes.Equal(slot.raw, []byte(`{"ok":true,"escaped":"a\r\nb"}`)) {
				t.Fatalf("result not observed: %s err=%v", slot.raw, err)
			}
		})
	}
}

func TestSSECRFramingBoundaries(t *testing.T) {
	for _, tc := range []struct{ in, want string }{{": x\r\n\r\n", ": x\r\n\r\n"}, {": x\r\r", ": x\r\n\r\n"}, {"\r\n\r\r\n\n", "\r\n\r\n\r\n\n"}, {": 未完成", ": 未完成"}, {": x\r", ": x\r\n"}} {
		o := NewObserver(WireLimits{})
		src := io.NopCloser(iotest.OneByteReader(strings.NewReader(tc.in)))
		b := &observedSSEBody{observer: o, source: src, reader: bufio.NewReader(src)}
		got, err := io.ReadAll(b)
		o.Close()
		if err != nil || string(got) != tc.want {
			t.Fatalf("%q => %q %v want %q", tc.in, got, err, tc.want)
		}
	}
	for _, limit := range []int{4, 5} {
		b := &observedSSEBody{reader: bufio.NewReader(strings.NewReader(": x\r"))}
		got, err := b.readSSELine(limit)
		if limit == 4 && !errors.Is(err, ErrWireLimit) {
			t.Fatalf("limit not enforced: %q %v", got, err)
		}
		if limit == 5 && (err != nil || string(got) != ": x\r\n") {
			t.Fatalf("exact limit rejected: %q %v", got, err)
		}
	}
}

func TestSSECRDeliversBeforeStreamClose(t *testing.T) {
	o := NewObserver(WireLimits{})
	defer o.Close()
	pr, pw := io.Pipe()
	defer pw.Close()
	defer pr.Close()
	b := &observedSSEBody{observer: o, source: pr, reader: bufio.NewReader(pr)}
	done := make(chan error, 1)
	go func() {
		p := make([]byte, 64)
		n, e := b.Read(p)
		if string(p[:n]) != ": heartbeat\r\n\r\n" {
			e = errors.New("wrong framing")
		}
		done <- e
	}()
	if _, err := pw.Write([]byte(": heartbeat\r\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CR event waited for next byte/EOF")
	}
	blocked := make(chan error, 1)
	go func() { _, e := b.Read(make([]byte, 1)); blocked <- e }()
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-blocked:
		if err == nil {
			t.Fatal("close did not interrupt read")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close blocked")
	}
}

func TestSSECRActualSDKConsumption(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	o := NewObserver(WireLimits{})
	defer o.Close()
	server := wireTestServer()
	addWireTool(server, "target", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "你好"}}}, nil
	})
	transport := &wireSSEFaultTransport{base: http.DefaultTransport, wrap: func(_ context.Context, src io.ReadCloser) (io.ReadCloser, error) {
		raw, err := io.ReadAll(src)
		src.Close()
		if err != nil {
			return nil, err
		}
		raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
		raw = bytes.ReplaceAll(raw, []byte("\n"), []byte("\r"))
		return io.NopCloser(iotest.OneByteReader(bytes.NewReader(raw))), nil
	}}
	session := newWireSSEFaultSession(t, ctx, server, o, transport)
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "target"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0].(*sdk.TextContent).Text != "你好" {
		t.Fatalf("result changed %+v", result)
	}
}

func TestSSECRWholeEventLimit(t *testing.T) {
	const event = ": a\r: b\r\r"
	const normalized = ": a\r\n: b\r\n\r\n"
	for _, limit := range []int{len(normalized) - 1, len(normalized)} {
		o := NewObserver(WireLimits{})
		o.limits.MaxFrameBytes = limit
		defer o.Close()
		src := io.NopCloser(strings.NewReader(event))
		body := &observedSSEBody{observer: o, source: src, reader: bufio.NewReader(src)}
		got, err := io.ReadAll(body)
		if limit < len(normalized) {
			if !errors.Is(err, ErrWireLimit) || len(got) != 0 {
				t.Fatalf("over-limit event leaked %q %v", got, err)
			}
		} else if err != nil || string(got) != normalized {
			t.Fatalf("exact event limit failed %q %v", got, err)
		}
	}
}

func BenchmarkSSELineFraming(b *testing.B) {
	for _, n := range []int{64 << 10, 1 << 20} {
		for _, ending := range []struct{ name, text string }{{"LF", "\n"}, {"CRLF", "\r\n"}, {"CR", "\r"}} {
			b.Run(fmt.Sprintf("%d/%s", n, ending.name), func(b *testing.B) {
				wire := strings.Repeat("x", n) + ending.text
				b.SetBytes(int64(len(wire)))
				b.ReportAllocs()
				for b.Loop() {
					r := &observedSSEBody{reader: bufio.NewReader(strings.NewReader(wire))}
					if _, err := r.readSSELine(n + 2); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
