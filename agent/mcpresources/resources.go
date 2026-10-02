// Package mcpresources exposes bounded resource operations through a managed
// MCP connection. It does not configure connections, identities or permissions.
package mcpresources

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"mime"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/internal/jsontext"
	managed "github.com/Icatme/pi-go/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	_ "golang.org/x/image/webp"
)

const (
	ListResourcesName         = "list_mcp_resources"
	ListResourceTemplatesName = "list_mcp_resource_templates"
	ReadResourceName          = "read_mcp_resource"
)

// Options bounds each invocation. Lists read exactly one page; the caller may
// pass nextCursor in a later invocation, subject to ordinary Agent call limits.
// Zero values select defaults; values above the supported ceilings fail.
type Options struct {
	ArtifactSink   ArtifactSink
	MaxItems       int
	MaxResultBytes int
	MaxBlobBytes   int
	MaxTextBytes   int
	MaxURIBytes    int
	MaxMIMEBytes   int
	MaxImagePixels int
}

func (o Options) defaults() (Options, error) {
	fields := []*int{&o.MaxItems, &o.MaxResultBytes, &o.MaxBlobBytes, &o.MaxTextBytes, &o.MaxURIBytes, &o.MaxMIMEBytes, &o.MaxImagePixels}
	defaults := []int{256, 1 << 20, 1 << 20, 512 << 10, 4096, 256, 64 << 20}
	ceilings := []int{256, 16 << 20, 8 << 20, 8 << 20, 16 << 10, 256, 64 << 20}
	for i, field := range fields {
		if *field < 0 || *field > ceilings[i] {
			return o, errors.New("mcpresources: unsupported limit")
		}
		if *field == 0 {
			*field = defaults[i]
		}
	}
	return o, nil
}

type arguments struct {
	Server string `json:"server"`
	Cursor string `json:"cursor,omitempty"`
	URI    string `json:"uri,omitempty"`
}

type resourceEntry struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

type templateEntry struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

// ReadContent keeps image data in structured output as well as model-facing
// image parts, so the Codemode native-value projection cannot lose an image.
type ReadContent struct {
	Type     string              `json:"type"`
	URI      string              `json:"uri"`
	MIMEType string              `json:"mimeType"`
	Text     string              `json:"text,omitempty"`
	Data     string              `json:"data,omitempty"`
	Artifact *ArtifactDescriptor `json:"artifact,omitempty"`
}

type readOutput struct {
	Server   string        `json:"server"`
	URI      string        `json:"uri"`
	Contents []ReadContent `json:"contents"`
}

// Definitions is inert: it reads only manager configuration and creates no
// processes, network requests, resource reads or artifacts. Scope and allowed
// server names are frozen; rebuild definitions after a trusted scope change.
func Definitions(manager *managed.Manager, options Options) ([]agent.ToolDefinition, error) {
	if manager == nil {
		return nil, errors.New("mcpresources: manager is required")
	}
	o, err := options.defaults()
	if err != nil {
		return nil, err
	}
	configuration, err := manager.ConfigSnapshot()
	if err != nil {
		return nil, err
	}
	scope := configuration.Scope()
	var servers []string
	for _, server := range configuration.Servers() {
		if !server.Disabled && server.Exposure != managed.Hidden {
			servers = append(servers, server.Name)
		}
	}
	if len(servers) == 0 {
		if !manager.IsCurrentConfig(configuration) {
			return nil, managed.ErrStale
		}
		return []agent.ToolDefinition{}, nil
	}
	binding, _ := json.Marshal(scope)
	digest := sha256.Sum256(binding)
	revision := "resource-scope:" + hex.EncodeToString(digest[:])
	definitions := make([]agent.ToolDefinition, 0, 3)
	for _, name := range []string{ListResourcesName, ListResourceTemplatesName, ReadResourceName} {
		definitions = append(definitions, agent.ToolDefinition{
			Name: name, Revision: revision, Description: resourceDescription(name),
			Parameters: resourceParameters(name, servers, o), OutputSchema: resourceOutputSchema(name),
			ParseArguments: func(call agent.ToolCall) (any, error) {
				var value any
				if call.ParsedArgs != nil {
					value = call.ParsedArgs
				}
				args, err := parseArguments(call.Arguments, value, name, o)
				if err != nil {
					return nil, before(agent.ToolFailureArgumentInvalid, "arguments", err)
				}
				return args, nil
			},
			Execute: func(ctx context.Context, execution agent.ToolExecutionContext) (agent.ToolResult, error) {
				args, err := parseArguments(nil, execution.Args, name, o)
				if err != nil {
					return agent.ToolResult{}, before(agent.ToolFailureArgumentInvalid, "arguments", err)
				}
				if err := checkScope(manager, scope, args.Server); err != nil {
					return agent.ToolResult{}, before(agent.ToolFailurePolicyDenied, "scope_or_exposure_changed", err)
				}
				if err := checkPermission(ctx, execution); err != nil {
					return agent.ToolResult{}, before(permissionCode(err), "permission", err)
				}
				connection, err := manager.Connect(ctx, args.Server)
				if err != nil {
					code := agent.ToolFailureProtocol
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						code = permissionCode(err)
					}
					return agent.ToolResult{}, before(code, "connection_unavailable", err)
				}
				if err := checkConnection(manager, connection, scope, args.Server); err != nil {
					return agent.ToolResult{}, before(agent.ToolFailurePolicyDenied, "scope_or_exposure_changed", err)
				}
				// Connection setup may wait; permission is checked again adjacent to
				// the actual SDK call rather than treating setup as authorization.
				if err := checkPermission(ctx, execution); err != nil {
					return agent.ToolResult{}, before(permissionCode(err), "permission", err)
				}
				tracked, snapshot := connection.Track(ctx)
				tracked = managed.WithDispatchCheck(tracked, func(sendCtx context.Context) error {
					if err := checkConnection(manager, connection, scope, args.Server); err != nil {
						return err
					}
					return checkPermission(sendCtx, execution)
				})
				var converted agent.ToolResult
				switch name {
				case ListResourcesName:
					response, callErr := connection.ListResources(tracked, &sdk.ListResourcesParams{Cursor: args.Cursor})
					if callErr != nil {
						return agent.ToolResult{}, callFailure(callErr, snapshot())
					}
					converted, err = convertResources(args.Server, args.Cursor, response, o)
				case ListResourceTemplatesName:
					response, callErr := connection.ListResourceTemplates(tracked, &sdk.ListResourceTemplatesParams{Cursor: args.Cursor})
					if callErr != nil {
						return agent.ToolResult{}, callFailure(callErr, snapshot())
					}
					converted, err = convertTemplates(args.Server, args.Cursor, response, o)
				case ReadResourceName:
					response, callErr := connection.ReadResource(tracked, &sdk.ReadResourceParams{URI: args.URI})
					if callErr != nil {
						return agent.ToolResult{}, callFailure(callErr, snapshot())
					}
					if response != nil && response.NeedsInput() {
						info := dispatchInfo(snapshot(), agent.ToolRemoteInputRequired)
						return agent.ToolResult{Execution: &info}, failure(agent.ToolFailureInputRequiredUnsupported, "input_required", info, errors.New("mcpresources: interactive resource input is unsupported"))
					}
					if err := checkConnection(manager, connection, scope, args.Server); err != nil {
						return agent.ToolResult{}, failure(agent.ToolFailurePolicyDenied, "scope_or_exposure_changed", dispatchInfo(snapshot(), agent.ToolRemoteCompleteReported), err)
					}
					if err := checkPermission(ctx, execution); err != nil {
						return agent.ToolResult{}, failure(permissionCode(err), "permission", dispatchInfo(snapshot(), agent.ToolRemoteCompleteReported), err)
					}
					if err := checkConnection(manager, connection, scope, args.Server); err != nil {
						return agent.ToolResult{}, failure(agent.ToolFailurePolicyDenied, "scope_or_exposure_changed", dispatchInfo(snapshot(), agent.ToolRemoteCompleteReported), err)
					}
					converted, err = convertRead(ctx, scope, args.Server, args.URI, response, o, func() error {
						if err := checkConnection(manager, connection, scope, args.Server); err != nil {
							return failure(agent.ToolFailurePolicyDenied, "scope_or_exposure_changed", dispatchInfo(snapshot(), agent.ToolRemoteCompleteReported), err)
						}
						if err := checkPermission(ctx, execution); err != nil {
							return failure(permissionCode(err), "permission", dispatchInfo(snapshot(), agent.ToolRemoteCompleteReported), err)
						}
						// Approval may wait while the host retires this identity or
						// policy. Validate again before handing bytes to the sink.
						if err := checkConnection(manager, connection, scope, args.Server); err != nil {
							return failure(agent.ToolFailurePolicyDenied, "scope_or_exposure_changed", dispatchInfo(snapshot(), agent.ToolRemoteCompleteReported), err)
						}
						return nil
					})
				}
				info := dispatchInfo(snapshot(), agent.ToolRemoteCompleteReported)
				converted.Execution = &info
				if err != nil {
					var typed *agent.ToolExecutionError
					if errors.As(err, &typed) {
						return converted, typed
					}
					code := agent.ToolFailureResultRejected
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						code = permissionCode(err)
					}
					return converted, failure(code, "resource_result", info, err)
				}
				if err := checkConnection(manager, connection, scope, args.Server); err != nil {
					return agent.ToolResult{Execution: &info}, failure(agent.ToolFailurePolicyDenied, "scope_or_exposure_changed", info, err)
				}
				if err := checkPermission(ctx, execution); err != nil {
					return agent.ToolResult{Execution: &info}, failure(permissionCode(err), "permission", info, err)
				}
				// The last approval can suspend. Do not publish the old result
				// after a completed scope or exposure change during that wait.
				if err := checkConnection(manager, connection, scope, args.Server); err != nil {
					return agent.ToolResult{Execution: &info}, failure(agent.ToolFailurePolicyDenied, "scope_or_exposure_changed", info, err)
				}
				return converted, nil
			},
		})
	}
	if !manager.IsCurrentConfig(configuration) {
		return nil, managed.ErrStale
	}
	return definitions, nil
}

func parseArguments(raw []byte, value any, name string, o Options) (arguments, error) {
	var args arguments
	if len(raw) > 0 {
		if len(raw) > 64<<10 {
			return args, errors.New("mcpresources: invalid argument size")
		}
		if err := jsontext.ValidateUnicode(raw); err != nil {
			return args, err
		}
	}
	if value != nil {
		if err := jsontext.ValidateStrings(value); err != nil {
			return args, err
		}
		var err error
		raw, err = json.Marshal(value)
		if err != nil {
			return args, errors.New("mcpresources: invalid arguments")
		}
	}
	if len(raw) == 0 || len(raw) > 64<<10 {
		return args, errors.New("mcpresources: invalid argument size")
	}
	if err := jsontext.ValidateUnicode(raw); err != nil {
		return args, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&args); err != nil || args.Server == "" || len(args.Server) > 128 {
		return args, errors.New("mcpresources: server and operation arguments are required")
	}
	if name == ReadResourceName {
		if args.Cursor != "" || validURI(args.URI, o.MaxURIBytes) != nil {
			return args, errors.New("mcpresources: read requires a bounded absolute URI")
		}
	} else if args.URI != "" || len(args.Cursor) > o.MaxURIBytes {
		return args, errors.New("mcpresources: list requires an optional bounded cursor")
	}
	return args, nil
}

func checkScope(manager *managed.Manager, scope managed.Scope, name string) error {
	configuration, err := manager.ConfigSnapshot()
	if err != nil {
		return err
	}
	if configuration.Scope() != scope {
		return managed.ErrStale
	}
	for _, server := range configuration.Servers() {
		if server.Name == name && !server.Disabled && server.Exposure != managed.Hidden {
			if !manager.IsCurrentConfig(configuration) {
				return managed.ErrStale
			}
			return nil
		}
	}
	return managed.ErrHidden
}

func checkConnection(manager *managed.Manager, connection *managed.Connection, scope managed.Scope, name string) error {
	if err := checkScope(manager, scope, name); err != nil {
		return err
	}
	if connection.Scope() != scope {
		return managed.ErrStale
	}
	current, err := manager.Ready(name)
	if err != nil || current != connection {
		return managed.ErrStale
	}
	return nil
}

func checkPermission(ctx context.Context, execution agent.ToolExecutionContext) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if execution.CheckPermission != nil {
		if err := execution.CheckPermission(ctx); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func convertResources(server, cursor string, response *sdk.ListResourcesResult, o Options) (agent.ToolResult, error) {
	if response == nil || len(response.Resources) > o.MaxItems || !validCursor(cursor, response.NextCursor, o) {
		return agent.ToolResult{}, errors.New("mcpresources: invalid or oversized resource page")
	}
	entries := make([]resourceEntry, 0, len(response.Resources))
	seen := make(map[string]bool)
	for _, resource := range response.Resources {
		if resource == nil || validURI(resource.URI, o.MaxURIBytes) != nil || seen[resource.URI] || resource.Size < 0 || resource.Size >= 1<<53 || resource.Name == "" {
			return agent.ToolResult{}, errors.New("mcpresources: invalid resource entry")
		}
		if _, err := validMIME(resource.MIMEType, o.MaxMIMEBytes, ""); err != nil {
			return agent.ToolResult{}, err
		}
		seen[resource.URI] = true
		entries = append(entries, resourceEntry{URI: resource.URI, Name: resource.Name, Title: resource.Title, Description: resource.Description, MIMEType: resource.MIMEType, Size: resource.Size})
	}
	return structuredResult(map[string]any{"server": server, "resources": entries, "nextCursor": response.NextCursor}, nil, o)
}

func convertTemplates(server, cursor string, response *sdk.ListResourceTemplatesResult, o Options) (agent.ToolResult, error) {
	if response == nil || len(response.ResourceTemplates) > o.MaxItems || !validCursor(cursor, response.NextCursor, o) {
		return agent.ToolResult{}, errors.New("mcpresources: invalid or oversized template page")
	}
	entries := make([]templateEntry, 0, len(response.ResourceTemplates))
	seen := make(map[string]bool)
	for _, template := range response.ResourceTemplates {
		if template == nil || !validTemplate(template.URITemplate, o.MaxURIBytes) || seen[template.URITemplate] || template.Name == "" {
			return agent.ToolResult{}, errors.New("mcpresources: invalid template entry")
		}
		if _, err := validMIME(template.MIMEType, o.MaxMIMEBytes, ""); err != nil {
			return agent.ToolResult{}, err
		}
		seen[template.URITemplate] = true
		entries = append(entries, templateEntry{URITemplate: template.URITemplate, Name: template.Name, Title: template.Title, Description: template.Description, MIMEType: template.MIMEType})
	}
	return structuredResult(map[string]any{"server": server, "resourceTemplates": entries, "nextCursor": response.NextCursor}, nil, o)
}

func convertRead(ctx context.Context, scope managed.Scope, server, uri string, response *sdk.ReadResourceResult, o Options, beforeArtifact func() error) (agent.ToolResult, error) {
	if response == nil || len(response.Contents) > o.MaxItems {
		return agent.ToolResult{}, errors.New("mcpresources: invalid or oversized read result")
	}
	output := readOutput{Server: server, URI: uri, Contents: make([]ReadContent, 0, len(response.Contents))}
	var parts []agent.Part
	textBytes, blobBytes := 0, 0
	// Validate every entry before writing an artifact; a later malformed entry
	// must not leave a partially exported result merely because it came last.
	for _, content := range response.Contents {
		if content == nil || validURI(content.URI, o.MaxURIBytes) != nil || (content.Text != "" && content.Blob != nil) {
			return agent.ToolResult{}, errors.New("mcpresources: malformed resource contents")
		}
		kind := "text"
		fallback := "text/plain"
		if content.Blob != nil {
			kind, fallback = "artifact", "application/octet-stream"
		}
		mimeType, err := validMIME(content.MIMEType, o.MaxMIMEBytes, fallback)
		if err != nil {
			return agent.ToolResult{}, err
		}
		entry := ReadContent{Type: kind, URI: content.URI, MIMEType: mimeType}
		if content.Blob == nil {
			if !utf8.ValidString(content.Text) {
				return agent.ToolResult{}, errors.New("mcpresources: invalid text Unicode")
			}
			textBytes += len(content.Text)
			if textBytes > o.MaxTextBytes {
				return agent.ToolResult{}, errors.New("mcpresources: text byte limit exceeded")
			}
			entry.Text = content.Text
			parts = append(parts, agent.NewTextPart(content.Text))
		} else {
			blobBytes += len(content.Blob)
			if blobBytes > o.MaxBlobBytes {
				return agent.ToolResult{}, errors.New("mcpresources: blob byte limit exceeded")
			}
			if supportedImage(mimeType) {
				if err := validImage(content.Blob, mimeType, o.MaxImagePixels); err != nil {
					return agent.ToolResult{}, err
				}
				entry.Type, entry.Data = "image", base64.StdEncoding.EncodeToString(content.Blob)
				parts = append(parts, agent.NewImagePart(entry.Data, mimeType))
			} else if o.ArtifactSink == nil {
				return agent.ToolResult{}, errors.New("mcpresources: binary resource requires an explicit artifact sink")
			}
		}
		output.Contents = append(output.Contents, entry)
	}
	// Reserve the full descriptor and text-part ceiling before any artifact
	// write; publishing a binary file must not be followed by a predictable
	// output-limit failure due to its generated descriptor.
	preflightParts := append([]agent.Part(nil), parts...)
	for i, entry := range output.Contents {
		if entry.Type == "artifact" {
			descriptor := ArtifactDescriptor{
				ID: strings.Repeat("a", 128), ScopeKey: artifactScopeKey(scope), Scope: scope,
				Server: server, URI: entry.URI, MIMEType: entry.MIMEType,
				SHA256: artifactDigest(response.Contents[i].Blob), Bytes: len(response.Contents[i].Blob),
			}
			output.Contents[i].Artifact = &descriptor
			preflightParts = append(preflightParts, agent.NewTextPart("MCP resource artifact: "+descriptor.ID))
		}
	}
	if _, err := structuredResult(output, preflightParts, o); err != nil {
		return agent.ToolResult{}, err
	}
	for i, entry := range output.Contents {
		if entry.Type != "artifact" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return agent.ToolResult{}, err
		}
		if beforeArtifact != nil {
			if err := beforeArtifact(); err != nil {
				return agent.ToolResult{}, err
			}
		}
		artifact := ResourceArtifact{Scope: scope, Server: server, URI: entry.URI, MIMEType: entry.MIMEType, Data: append([]byte(nil), response.Contents[i].Blob...)}
		artifact.SHA256 = artifactDigest(artifact.Data)
		descriptor, err := o.ArtifactSink.WriteArtifact(ctx, artifact)
		if err != nil {
			return agent.ToolResult{}, fmt.Errorf("mcpresources: host artifact export failed: %w", err)
		}
		if err := validateDescriptor(artifact, descriptor); err != nil {
			return agent.ToolResult{}, err
		}
		output.Contents[i].Artifact = &descriptor
		parts = append(parts, agent.NewTextPart("MCP resource artifact: "+descriptor.ID))
	}
	return structuredResult(output, parts, o)
}

func structuredResult(value any, parts []agent.Part, o Options) (agent.ToolResult, error) {
	if err := jsontext.ValidateStrings(value); err != nil {
		return agent.ToolResult{}, err
	}
	data, err := json.Marshal(value)
	if err != nil || len(data) > o.MaxResultBytes {
		return agent.ToolResult{}, errors.New("mcpresources: result byte limit exceeded")
	}
	if parts == nil {
		parts = []agent.Part{agent.NewTextPart(string(data))}
	}
	result := agent.ToolResult{Content: parts, StructuredContent: data}
	if output, ok := value.(readOutput); ok {
		var descriptors []ArtifactDescriptor
		for _, content := range output.Contents {
			if content.Artifact != nil {
				descriptors = append(descriptors, *content.Artifact)
			}
		}
		if len(descriptors) > 0 {
			result.Details = descriptors
		}
	}
	// Count both structured output and model-facing content: image/text copies
	// must not evade the aggregate output bound through a second representation.
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > o.MaxResultBytes {
		return agent.ToolResult{}, errors.New("mcpresources: aggregate result byte limit exceeded")
	}
	return result, nil
}

func validURI(value string, limit int) error {
	if value == "" || len(value) > limit || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n\t ") {
		return errors.New("mcpresources: invalid resource URI")
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" {
		return errors.New("mcpresources: resource URI must be absolute")
	}
	return nil // Resource schemes are data; this client never fetches URI assets.
}

func validMIME(value string, limit int, fallback string) (string, error) {
	if value == "" {
		return fallback, nil
	}
	if len(value) > limit || !utf8.ValidString(value) {
		return "", errors.New("mcpresources: invalid MIME type")
	}
	typeName, _, err := mime.ParseMediaType(value)
	if err != nil || !strings.Contains(typeName, "/") {
		return "", errors.New("mcpresources: invalid MIME type")
	}
	return typeName, nil
}

func validTemplate(value string, limit int) bool {
	if value == "" || len(value) > limit || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n\t ") {
		return false
	}
	colon := strings.IndexByte(value, ':')
	if colon <= 0 {
		return false
	}
	for i, r := range value[:colon] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && (r >= '0' && r <= '9' || strings.ContainsRune("+.-", r))) {
			return false
		}
	}
	return true // A URI template is returned as data, never evaluated here.
}

func validCursor(previous, next string, o Options) bool {
	return len(next) <= o.MaxURIBytes && utf8.ValidString(next) && (next == "" || next != previous)
}

func supportedImage(mimeType string) bool {
	return mimeType == "image/png" || mimeType == "image/jpeg" || mimeType == "image/gif" || mimeType == "image/webp"
}

func validImage(data []byte, mimeType string, pixels int) error {
	if mimeType == "image/webp" && (len(data) < 20 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" || int(binary.LittleEndian.Uint32(data[4:8])) != len(data)-8) {
		return errors.New("mcpresources: invalid WebP image")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != strings.TrimPrefix(mimeType, "image/") || config.Width <= 0 || config.Height <= 0 || int64(config.Width) > int64(pixels)/int64(config.Height) {
		return errors.New("mcpresources: unsupported or oversized image")
	}
	return nil
}

func before(code agent.ToolFailureCode, reason string, err error) error {
	return failure(code, reason, agent.ToolExecutionInfo{Remote: agent.ToolRemoteNotDispatched}, err)
}

func failure(code agent.ToolFailureCode, reason string, info agent.ToolExecutionInfo, err error) error {
	return &agent.ToolExecutionError{Code: code, Reason: reason, Execution: info, Message: "MCP resource operation rejected: " + reason, Err: err}
}

func permissionCode(err error) agent.ToolFailureCode {
	if errors.Is(err, context.Canceled) {
		return agent.ToolFailureCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return agent.ToolFailureDeadline
	}
	return agent.ToolFailurePolicyDenied
}

func dispatchInfo(record managed.DispatchRecord, remote agent.ToolRemoteState) agent.ToolExecutionInfo {
	info := agent.ToolExecutionInfo{Remote: remote}
	if record.Attempts > 0 {
		info.Attempts = []agent.ToolSendAttempt{{Number: record.Attempts, Remote: remote}}
	}
	return info
}

func callFailure(err error, record managed.DispatchRecord) error {
	remote := agent.ToolRemoteUnknown
	if record.Attempts == 0 {
		remote = agent.ToolRemoteNotDispatched
	}
	var response *managed.ResponseError
	if errors.As(err, &response) {
		remote = agent.ToolRemoteCompleteReported
		if response.NeedsInput {
			remote = agent.ToolRemoteInputRequired
		}
	} else if record.ResultType == "input_required" {
		remote = agent.ToolRemoteInputRequired
	} else if record.ResultType == "complete" {
		remote = agent.ToolRemoteCompleteReported
	}
	code := agent.ToolFailureTransport
	if remote == agent.ToolRemoteCompleteReported || remote == agent.ToolRemoteInputRequired {
		code = agent.ToolFailureResultRejected
	}
	if errors.Is(err, context.Canceled) {
		code = agent.ToolFailureCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		code = agent.ToolFailureDeadline
	}
	var denied *managed.DispatchDeniedError
	if errors.As(err, &denied) {
		// Observer teardown may also cancel the SDK waiter. Classify the
		// authoritative dispatch denial rather than that secondary cancellation.
		code = permissionCode(denied.Err)
	}
	return failure(code, "resource_call", dispatchInfo(record, remote), err)
}

func resourceDescription(name string) string {
	switch name {
	case ListResourcesName:
		return "List one page of an allowed MCP server's resources; pass nextCursor to read another page."
	case ListResourceTemplatesName:
		return "List one page of an allowed MCP server's URI templates; pass nextCursor to read another page."
	default:
		return "Read an allowed MCP resource as bounded text, validated inline image, or host artifact descriptor. URI assets are never fetched by the client."
	}
}

func resourceParameters(name string, servers []string, o Options) map[string]any {
	properties := map[string]any{"server": map[string]any{"type": "string", "enum": append([]string(nil), servers...)}}
	required := []string{"server"}
	if name == ReadResourceName {
		properties["uri"] = map[string]any{"type": "string", "minLength": 1, "maxLength": o.MaxURIBytes}
		required = append(required, "uri")
	} else {
		properties["cursor"] = map[string]any{"type": "string", "maxLength": o.MaxURIBytes}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

func resourceOutputSchema(name string) map[string]any {
	properties := map[string]any{"server": map[string]any{"type": "string"}}
	required := []string{"server"}
	switch name {
	case ListResourcesName:
		properties["resources"] = map[string]any{"type": "array", "items": map[string]any{"type": "object"}}
		properties["nextCursor"] = map[string]any{"type": "string"}
		required = append(required, "resources")
	case ListResourceTemplatesName:
		properties["resourceTemplates"] = map[string]any{"type": "array", "items": map[string]any{"type": "object"}}
		properties["nextCursor"] = map[string]any{"type": "string"}
		required = append(required, "resourceTemplates")
	case ReadResourceName:
		properties["uri"] = map[string]any{"type": "string"}
		properties["contents"] = map[string]any{"type": "array", "items": map[string]any{"type": "object"}}
		required = append(required, "uri", "contents")
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
