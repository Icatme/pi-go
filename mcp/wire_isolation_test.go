package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestReadResourceCachedResultDetached(t *testing.T) {
	server := wireTestServer()
	var reads atomic.Int32
	server.AddResource(&sdk.Resource{URI: "test://blob", Name: "blob"}, func(context.Context, *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		reads.Add(1)
		return &sdk.ReadResourceResult{Meta: sdk.Meta{"nested": map[string]any{"value": "original"}}, Contents: []*sdk.ResourceContents{{URI: "test://blob", Blob: []byte("original"), Meta: sdk.Meta{"nested": map[string]any{"value": "original"}}}}}, nil
	})
	_, conn := newManagerFixture(t, server, true)
	first, err := conn.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: "test://blob"})
	if err != nil {
		t.Fatal(err)
	}
	first.Contents[0].Blob[0] = '!'
	first.Meta["nested"].(map[string]any)["value"] = "changed"
	first.Contents[0].Meta["nested"].(map[string]any)["value"] = "changed"
	second, err := conn.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: "test://blob"})
	if err != nil {
		t.Fatal(err)
	}
	if reads.Load() != 1 {
		t.Fatalf("fixture not cached: reads=%d", reads.Load())
	}
	if got := string(second.Contents[0].Blob); got != "original" {
		t.Fatalf("caller mutation corrupted cached resource: got %q, want original", got)
	}
	if second.Meta["nested"].(map[string]any)["value"] != "original" || second.Contents[0].Meta["nested"].(map[string]any)["value"] != "original" {
		t.Fatal("caller mutation corrupted cached metadata")
	}
	if first.TTLMs != second.TTLMs || first.NeedsInput() != second.NeedsInput() {
		t.Fatal("detachment changed SDK result lifecycle")
	}
}

func TestWireEnvelopeCaseSensitivityMatchesSDK(t *testing.T) {
	server := wireTestServer()
	addWireTool(server, "echo", func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "actual result"}}, StructuredContent: map[string]any{"source": "actual"}}, nil
	})
	httpServer := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer httpServer.Close()
	transport := managerTransportFunc(func(req *http.Request) (*http.Response, error) {
		var method string
		if req.Body != nil {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			req.Body.Close()
			req.Body = io.NopCloser(bytes.NewReader(body))
			var envelope struct{ Method string }
			if err := json.Unmarshal(body, &envelope); err != nil {
				return nil, err
			}
			method = envelope.Method
		}
		response, err := http.DefaultTransport.RoundTrip(req)
		if err != nil || method != "tools/call" {
			return response, err
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			return nil, err
		}
		body = bytes.TrimSpace(body)
		body = append(body[:len(body)-1], []byte(`,"ID":999,"METHOD":"unexpected","ERROR":{"code":-32000},"RESULT":{"structuredContent":{"source":"case-folded extra field"}}}`)...)
		response.Body = io.NopCloser(bytes.NewReader(body))
		response.ContentLength = int64(len(body))
		return response, nil
	})
	manager, err := New(Config{Scope: Scope{Identity: "review"}, HTTPClient: &http.Client{Transport: transport}, Servers: []ServerConfig{{Name: "fixture", URL: httpServer.URL, Timeout: 3 * time.Second}}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeManager(t, manager)
	conn, err := manager.Connect(t.Context(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	result, err := conn.CallTool(t.Context(), &sdk.CallToolParams{Name: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	if text := result.Content[0].(*sdk.TextContent).Text; text != "actual result" {
		t.Fatalf("SDK content unexpectedly changed: %q", text)
	}
	if got := result.StructuredContent.(map[string]any)["source"]; got != "actual" {
		t.Fatalf("raw observer disagrees with case-sensitive SDK: got %q, want actual", got)
	}
}
