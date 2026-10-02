package mcpresources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/codemodetool"
	"github.com/Icatme/pi-go/codemode"
	managed "github.com/Icatme/pi-go/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func resourceFixture(t *testing.T, server *sdk.Server, exposure managed.Exposure, disabled bool) (*managed.Manager, *atomic.Int32) {
	t.Helper()
	requests := new(atomic.Int32)
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(httpServer.Close)
	m, err := managed.New(managed.Config{
		Scope:   managed.Scope{Identity: "alice", AuthEpoch: 1},
		Servers: []managed.ServerConfig{{Name: "fixture", URL: httpServer.URL, Exposure: exposure, Disabled: disabled, Timeout: 5 * time.Second}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Errorf("manager close: %v", err)
		}
	})
	return m, requests
}

func resourceServer(t *testing.T, uri, mimeType string, contents []*sdk.ResourceContents) (*sdk.Server, *atomic.Int32) {
	t.Helper()
	reads := new(atomic.Int32)
	server := sdk.NewServer(&sdk.Implementation{Name: "resources", Version: "1"}, nil)
	server.AddResource(&sdk.Resource{Name: "fixture", URI: uri, MIMEType: mimeType}, func(context.Context, *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		reads.Add(1)
		return &sdk.ReadResourceResult{Contents: contents}, nil
	})
	return server, reads
}

func resourceDefinition(t *testing.T, manager *managed.Manager, options Options, name string) agent.ToolDefinition {
	t.Helper()
	definitions, err := Definitions(manager, options)
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		if definition.Name == name {
			return definition
		}
	}
	t.Fatalf("missing resource definition %s", name)
	return agent.ToolDefinition{}
}

func invokeResource(ctx context.Context, definition agent.ToolDefinition, args map[string]any, permission func(context.Context) error) (agent.ToolResult, error) {
	return definition.Execute(ctx, agent.ToolExecutionContext{Args: args, CheckPermission: permission})
}

func TestDefinitionsAreInertAndHiddenUnavailable(t *testing.T) {
	for _, test := range []struct {
		exposure managed.Exposure
		disabled bool
		count    int
	}{{managed.Codemode, false, 3}, {managed.Hidden, false, 0}, {managed.Codemode, true, 0}} {
		server, _ := resourceServer(t, "demo://text", "text/plain", []*sdk.ResourceContents{{Text: "text"}})
		m, requests := resourceFixture(t, server, test.exposure, test.disabled)
		definitions, err := Definitions(m, Options{})
		if err != nil || len(definitions) != test.count || requests.Load() != 0 {
			t.Fatalf("Definitions exposure=%s disabled=%v count=%d requests=%d err=%v", test.exposure, test.disabled, len(definitions), requests.Load(), err)
		}
	}
}

func TestListsReturnOneBoundedPageAndTemplates(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "resources", Version: "1"}, &sdk.ServerOptions{PageSize: 1})
	handler := func(context.Context, *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{Text: "text"}}}, nil
	}
	server.AddResource(&sdk.Resource{Name: "a", URI: "demo://a", MIMEType: "text/plain"}, handler)
	server.AddResource(&sdk.Resource{Name: "b", URI: "demo://b", MIMEType: "text/plain"}, handler)
	server.AddResourceTemplate(&sdk.ResourceTemplate{Name: "item", URITemplate: "demo://item/{id}", MIMEType: "text/plain"}, handler)
	m, _ := resourceFixture(t, server, managed.Codemode, false)
	definition := resourceDefinition(t, m, Options{}, ListResourcesName)
	result, err := invokeResource(t.Context(), definition, map[string]any{"server": "fixture"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Resources  []resourceEntry `json:"resources"`
		NextCursor string          `json:"nextCursor"`
	}
	if err := json.Unmarshal(result.StructuredContent, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Resources) != 1 || page.Resources[0].URI != "demo://a" || page.NextCursor == "" || result.Execution.Remote != agent.ToolRemoteCompleteReported {
		t.Fatalf("list auto-paginated or lost facts: result=%s execution=%+v", result.StructuredContent, result.Execution)
	}
	result, err = invokeResource(t.Context(), definition, map[string]any{"server": "fixture", "cursor": page.NextCursor}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(result.StructuredContent, &page); err != nil || len(page.Resources) != 1 || page.Resources[0].URI != "demo://b" || page.NextCursor != "" {
		t.Fatalf("second page: %s err=%v", result.StructuredContent, err)
	}
	result, err = invokeResource(t.Context(), resourceDefinition(t, m, Options{}, ListResourceTemplatesName), map[string]any{"server": "fixture"}, nil)
	if err != nil || !bytes.Contains(result.StructuredContent, []byte(`demo://item/{id}`)) {
		t.Fatalf("template list: %s err=%v", result.StructuredContent, err)
	}
}

func TestResourcePermissionRecheckedAfterConnectionSetup(t *testing.T) {
	server, reads := resourceServer(t, "demo://text", "text/plain", []*sdk.ResourceContents{{Text: "text"}})
	m, _ := resourceFixture(t, server, managed.Codemode, false)
	definition := resourceDefinition(t, m, Options{}, ReadResourceName)
	var checks atomic.Int32
	denied := errors.New("permission revoked")
	_, err := invokeResource(t.Context(), definition, map[string]any{"server": "fixture", "uri": "demo://text"}, func(context.Context) error {
		if checks.Add(1) >= 2 {
			return denied
		}
		return nil
	})
	var failure *agent.ToolExecutionError
	if !errors.As(err, &failure) || failure.Code != agent.ToolFailurePolicyDenied || failure.Execution.Remote != agent.ToolRemoteNotDispatched || reads.Load() != 0 {
		t.Fatalf("revoked permission dispatched resource: reads=%d err=%v facts=%+v", reads.Load(), err, failure)
	}
}

func TestResourcePermissionRecheckedAtPhysicalDispatch(t *testing.T) {
	server, reads := resourceServer(t, "demo://text", "text/plain", []*sdk.ResourceContents{{Text: "text"}})
	m, _ := resourceFixture(t, server, managed.Codemode, false)
	definition := resourceDefinition(t, m, Options{}, ReadResourceName)
	var checks atomic.Int32
	denied := errors.New("private permission reason")
	_, err := invokeResource(t.Context(), definition, map[string]any{"server": "fixture", "uri": "demo://text"}, func(context.Context) error {
		if checks.Add(1) >= 3 {
			return denied
		}
		return nil
	})
	var failure *agent.ToolExecutionError
	if !errors.As(err, &failure) || failure.Code != agent.ToolFailurePolicyDenied || failure.Execution.Remote != agent.ToolRemoteNotDispatched || reads.Load() != 0 || !errors.Is(err, denied) {
		t.Fatalf("physical dispatch ignored revocation or cause: reads=%d err=%v facts=%+v", reads.Load(), err, failure)
	}
}

func TestResourceScopeAndExposureChangesBlockFrozenDefinitions(t *testing.T) {
	for _, change := range []string{"identity", "hidden"} {
		t.Run(change, func(t *testing.T) {
			server, reads := resourceServer(t, "demo://text", "text/plain", []*sdk.ResourceContents{{Text: "text"}})
			m, requests := resourceFixture(t, server, managed.Codemode, false)
			definition := resourceDefinition(t, m, Options{}, ReadResourceName)
			if change == "identity" {
				if err := m.SetScope(managed.Scope{Identity: "bob", AuthEpoch: 2}); err != nil {
					t.Fatal(err)
				}
			} else if err := m.SetExposure("fixture", managed.Hidden, nil); err != nil {
				t.Fatal(err)
			}
			_, err := invokeResource(t.Context(), definition, map[string]any{"server": "fixture", "uri": "demo://text"}, nil)
			var failure *agent.ToolExecutionError
			if !errors.As(err, &failure) || failure.Code != agent.ToolFailurePolicyDenied || failure.Execution.Remote != agent.ToolRemoteNotDispatched || reads.Load() != 0 || requests.Load() != 0 {
				t.Fatalf("stale definition dispatched: err=%v facts=%+v requests=%d", err, failure, requests.Load())
			}
		})
	}
}

func TestReadTextAndImageSurviveCodemodeProjection(t *testing.T) {
	var pngData bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&pngData, img); err != nil {
		t.Fatal(err)
	}
	server, _ := resourceServer(t, "demo://image", "image/png", []*sdk.ResourceContents{{URI: "demo://image", MIMEType: "image/png", Blob: pngData.Bytes()}})
	m, _ := resourceFixture(t, server, managed.Codemode, false)
	definition := resourceDefinition(t, m, Options{}, ReadResourceName)
	result, err := invokeResource(t.Context(), definition, map[string]any{"server": "fixture", "uri": "demo://image"}, nil)
	if err != nil || len(result.Content) != 1 || result.Content[0].Type != agent.PartTypeImage || !bytes.Contains(result.StructuredContent, []byte(`"type":"image"`)) {
		t.Fatalf("image read lost content: result=%+v err=%v", result, err)
	}
	sandbox, err := codemode.NewSandbox(t.Context(), codemode.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := sandbox.Close(ctx); err != nil {
			t.Errorf("sandbox close: %v", err)
		}
	})
	binding := codemodetool.Native(definition, "mcp_resources")
	container, err := codemodetool.New(sandbox, []codemodetool.Binding{binding}, codemodetool.Options{})
	if err != nil {
		t.Fatal(err)
	}
	script := "const r = await tools." + binding.ExportedName() + `({server:"fixture",uri:"demo://image"}); image(r.contents[0]);`
	outcome := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "outer", Name: container.Name, ParsedArgs: map[string]any{"code": script}}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{container}})
	images := 0
	for _, part := range outcome.Result.Content {
		if part.Type == agent.PartTypeImage && part.Data == result.Content[0].Data {
			images++
		}
	}
	if outcome.Err != nil || outcome.IsError || images != 1 {
		t.Fatalf("Codemode native projection lost image: err=%v result=%+v", outcome.Err, outcome.Result)
	}
	textServer, _ := resourceServer(t, "demo://text", "text/plain", []*sdk.ResourceContents{{Text: "hello"}})
	textManager, _ := resourceFixture(t, textServer, managed.Codemode, false)
	textResult, err := invokeResource(t.Context(), resourceDefinition(t, textManager, Options{}, ReadResourceName), map[string]any{"server": "fixture", "uri": "demo://text"}, nil)
	if err != nil || textResult.Content[0].Text != "hello" {
		t.Fatalf("text read: %+v err=%v", textResult, err)
	}
}

func TestReadInputRequiredPreservesRemoteFacts(t *testing.T) {
	server, _ := resourceServer(t, "demo://input", "text/plain", []*sdk.ResourceContents{{Text: "unused"}})
	server.AddReceivingMiddleware(func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, request sdk.Request) (sdk.Result, error) {
			if method == "resources/read" {
				var result sdk.ReadResourceResult
				if err := json.Unmarshal([]byte(`{"resultType":"input_required","contents":null,"inputRequests":{}}`), &result); err != nil {
					return nil, err
				}
				return &result, nil
			}
			return next(ctx, method, request)
		}
	})
	m, _ := resourceFixture(t, server, managed.Codemode, false)
	_, err := invokeResource(t.Context(), resourceDefinition(t, m, Options{}, ReadResourceName), map[string]any{"server": "fixture", "uri": "demo://input"}, nil)
	var failure *agent.ToolExecutionError
	if !errors.As(err, &failure) || failure.Code != agent.ToolFailureInputRequiredUnsupported || failure.Execution.Remote != agent.ToolRemoteInputRequired {
		t.Fatalf("input required became completed: err=%v facts=%+v", err, failure)
	}
}

func TestCanceledResourceRetainsUncertainRemoteFacts(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	server := sdk.NewServer(&sdk.Implementation{Name: "resources", Version: "1"}, nil)
	server.AddResource(&sdk.Resource{Name: "slow", URI: "demo://slow", MIMEType: "text/plain"}, func(ctx context.Context, _ *sdk.ReadResourceRequest) (*sdk.ReadResourceResult, error) {
		close(entered)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{Text: "late"}}}, nil
		}
	})
	m, _ := resourceFixture(t, server, managed.Codemode, false)
	t.Cleanup(func() { close(release) })
	definition := resourceDefinition(t, m, Options{}, ReadResourceName)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := invokeResource(ctx, definition, map[string]any{"server": "fixture", "uri": "demo://slow"}, nil)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("resource handler did not enter")
	}
	cancel()
	select {
	case err := <-done:
		var failure *agent.ToolExecutionError
		if !errors.As(err, &failure) || failure.Execution.Remote != agent.ToolRemoteUnknown || failure.Code != agent.ToolFailureCanceled || len(failure.Execution.Attempts) != 1 {
			t.Fatalf("canceled read claimed not dispatched or complete: err=%v facts=%+v", err, failure)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled resource did not stop")
	}
}

func TestResourceResultLimitsAndInvalidImage(t *testing.T) {
	for _, test := range []struct {
		name     string
		mimeType string
		content  *sdk.ResourceContents
		options  Options
	}{
		{"text", "text/plain", &sdk.ResourceContents{Text: "oversized"}, Options{MaxTextBytes: 4}},
		{"blob", "application/octet-stream", &sdk.ResourceContents{Blob: []byte("oversized")}, Options{MaxBlobBytes: 4}},
		{"image", "image/png", &sdk.ResourceContents{Blob: []byte("not a PNG")}, Options{}},
		{"mime", "application/invalid; broken", &sdk.ResourceContents{Text: "text"}, Options{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, reads := resourceServer(t, "demo://bounded", test.mimeType, []*sdk.ResourceContents{test.content})
			m, _ := resourceFixture(t, server, managed.Codemode, false)
			_, err := invokeResource(t.Context(), resourceDefinition(t, m, test.options, ReadResourceName), map[string]any{"server": "fixture", "uri": "demo://bounded"}, nil)
			var failure *agent.ToolExecutionError
			if !errors.As(err, &failure) || failure.Code != agent.ToolFailureResultRejected || failure.Execution.Remote != agent.ToolRemoteCompleteReported || reads.Load() != 1 {
				t.Fatalf("unsafe result not rejected after response: err=%v facts=%+v reads=%d", err, failure, reads.Load())
			}
		})
	}
}

func TestBinaryResourcesRequireSinkAndPublishBoundDescriptor(t *testing.T) {
	data := []byte("<html><script>must never execute</script></html>")
	uri := "demo://binary/../../outside.html"
	server, _ := resourceServer(t, uri, "application/octet-stream", []*sdk.ResourceContents{{URI: uri, Blob: data}})
	m, _ := resourceFixture(t, server, managed.Codemode, false)
	_, err := invokeResource(t.Context(), resourceDefinition(t, m, Options{}, ReadResourceName), map[string]any{"server": "fixture", "uri": uri}, nil)
	if err == nil {
		t.Fatal("binary resource entered context without sink")
	}
	root := t.TempDir()
	store, err := NewFileArtifactStore(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	result, err := invokeResource(t.Context(), resourceDefinition(t, m, Options{ArtifactSink: store}, ReadResourceName), map[string]any{"server": "fixture", "uri": uri}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(result.StructuredContent, data) || bytes.Contains(result.StructuredContent, []byte(root)) {
		t.Fatalf("binary/path leaked into structured result: %s", result.StructuredContent)
	}
	descriptors, ok := result.Details.([]ArtifactDescriptor)
	if !ok || len(descriptors) != 1 {
		t.Fatalf("missing host descriptor: %T", result.Details)
	}
	descriptor := descriptors[0]
	if descriptor.Scope != m.Scope() || descriptor.URI != uri || descriptor.MIMEType != "application/octet-stream" || descriptor.SHA256 != artifactDigest(data) || descriptor.Bytes != len(data) || filepath.Dir(descriptor.Path) != root || strings.Contains(filepath.Base(descriptor.Path), "outside") || filepath.Ext(descriptor.Path) != ".blob" {
		t.Fatalf("artifact binding or filename unsafe: %+v", descriptor)
	}
	stored, err := os.ReadFile(descriptor.Path)
	if err != nil || !bytes.Equal(stored, data) {
		t.Fatalf("artifact bytes: err=%v", err)
	}
}

func TestCanceledArtifactAndPredictableOutputFailureLeaveNoFiles(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileArtifactStore(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	artifact := ResourceArtifact{Scope: managed.Scope{Identity: "alice", AuthEpoch: 1}, Server: "fixture", URI: "demo://blob", MIMEType: "application/octet-stream", Data: []byte("blob")}
	artifact.SHA256 = artifactDigest(artifact.Data)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.WriteArtifact(ctx, artifact); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled artifact: %v", err)
	}
	server, _ := resourceServer(t, "demo://blob", "application/octet-stream", []*sdk.ResourceContents{{Blob: []byte("blob")}})
	m, _ := resourceFixture(t, server, managed.Codemode, false)
	_, err = invokeResource(t.Context(), resourceDefinition(t, m, Options{ArtifactSink: store, MaxResultBytes: 200}, ReadResourceName), map[string]any{"server": "fixture", "uri": "demo://blob"}, nil)
	if err == nil {
		t.Fatal("descriptor output limit was not enforced")
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 0 {
		t.Fatalf("artifact persisted after cancellation/predictable output failure: files=%v err=%v", files, err)
	}
}

type artifactSinkFunc func(context.Context, ResourceArtifact) (ArtifactDescriptor, error)

func (f artifactSinkFunc) WriteArtifact(ctx context.Context, artifact ResourceArtifact) (ArtifactDescriptor, error) {
	return f(ctx, artifact)
}

func TestArtifactSinkErrorPreservesCancellationAndRedactsCause(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, errors.New("private-sink-secret")} {
		server, _ := resourceServer(t, "demo://blob", "application/octet-stream", []*sdk.ResourceContents{{Blob: []byte("blob")}})
		m, _ := resourceFixture(t, server, managed.Codemode, false)
		sink := artifactSinkFunc(func(context.Context, ResourceArtifact) (ArtifactDescriptor, error) {
			return ArtifactDescriptor{}, cause
		})
		definition := resourceDefinition(t, m, Options{ArtifactSink: sink}, ReadResourceName)
		result := agent.RunToolCall(t.Context(), agent.ToolCall{ID: "read", Name: definition.Name, ParsedArgs: map[string]any{"server": "fixture", "uri": "demo://blob"}}, agent.RunToolCallOptions{Tools: []agent.ToolDefinition{definition}})
		var failure *agent.ToolExecutionError
		if !result.IsError || !errors.Is(result.Err, cause) || !errors.As(result.Err, &failure) || failure.Execution.Remote != agent.ToolRemoteCompleteReported {
			t.Fatalf("sink cause or remote completion lost: err=%v failure=%+v", result.Err, failure)
		}
		want := agent.ToolFailureResultRejected
		if errors.Is(cause, context.Canceled) {
			want = agent.ToolFailureCanceled
		} else if errors.Is(cause, context.DeadlineExceeded) {
			want = agent.ToolFailureDeadline
		}
		if failure.Code != want {
			t.Fatalf("sink failure classified %s, want %s", failure.Code, want)
		}
		for _, part := range result.Result.Content {
			if strings.Contains(part.Text, "private-sink-secret") {
				t.Fatal("private sink cause leaked into model-facing output")
			}
		}
	}
}
