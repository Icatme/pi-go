package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/Icatme/pi-go/internal/jsontext"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Catalog is an immutable, detached directory snapshot. Scope and Generation
// bind it to the actual SDK session; Revision changes after directory changes.
type Catalog struct {
	Server       string
	Scope        Scope
	Generation   uint64
	Revision     uint64
	Protocol     string
	Description  string
	Instructions string
	Resources    bool
	SourceDigest string
	Tools        []*sdk.Tool
}

// A frozen catalog owns decoded pages and their original tool JSON. SDK cache
// hits reuse the observer's immutable binding identity; callers receive fresh
// decoded tools, never storage owned by the frozen catalog or SDK.
type frozenCatalog struct {
	Catalog
	pages []*catalogPage
}

type catalogPage struct {
	cursor string
	source *rawBinding
	result sdk.ListToolsResult
	tools  []json.RawMessage
}

func (c *frozenCatalog) page(cursor string) *catalogPage {
	if c != nil {
		for _, page := range c.pages {
			if page.cursor == cursor {
				return page
			}
		}
	}
	return nil
}

type Connection struct {
	config       ServerConfig
	scope        Scope
	generation   uint64
	limits       Limits
	session      *sdk.ClientSession
	sessionDone  chan struct{}
	sessionErr   error // published by closing sessionDone
	observer     *Observer
	oauth        *OAuth
	process      *managedProcess
	ctx          context.Context
	cancel       context.CancelFunc
	closed       atomic.Bool
	stale        atomic.Bool
	closeOnce    sync.Once
	closeDone    chan struct{}
	closeErr     error
	authMu       sync.Mutex
	authState    OAuthState
	authPending  atomic.Int64
	revision     atomic.Uint64
	refreshMu    sync.Mutex
	mu           sync.RWMutex
	catalog      *frozenCatalog
	catalogReads int // real external ListTools requests awaiting validation
}

type headerTransport struct {
	base    http.RoundTripper
	headers http.Header
	valid   func() error
}

func (h headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if h.valid != nil && req.Method != http.MethodDelete {
		if err := h.valid(); err != nil {
			return nil, err
		}
	}
	r := req.Clone(req.Context())
	r.Header = req.Header.Clone()
	for k, v := range h.headers {
		r.Header[k] = append([]string(nil), v...)
	}
	return h.base.RoundTrip(r)
}

func connect(ctx context.Context, s ServerConfig, scope Scope, generation uint64, cfg Config) (*Connection, error) {
	life, cancel := context.WithCancel(ctx)
	c := &Connection{config: s, scope: scope, generation: generation, limits: cfg.Limits, ctx: life, cancel: cancel, observer: NewObserver(cfg.Limits.Wire), closeDone: make(chan struct{}), sessionDone: make(chan struct{})}
	c.revision.Store(1)
	client := sdk.NewClient(&sdk.Implementation{Name: "pi-go", Version: "1"}, &sdk.ClientOptions{
		Capabilities: &sdk.ClientCapabilities{}, MultiRoundTrip: &sdk.MultiRoundTripOptions{Disabled: true},
		ToolListChangedHandler:     func(context.Context, *sdk.ToolListChangedRequest) { c.invalidate() },
		ResourceListChangedHandler: func(context.Context, *sdk.ResourceListChangedRequest) { c.invalidate() },
	})
	client.AddSendingMiddleware(c.observer.Middleware())
	var transport sdk.Transport
	if s.URL != "" {
		hc := &http.Client{}
		if cfg.HTTPClient != nil {
			*hc = *cfg.HTTPClient
		}
		base := hc.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		hc.Transport = c.observer.RoundTripper(headerTransport{base: base, headers: s.Headers, valid: c.available})
		hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		t := &sdk.StreamableClientTransport{Endpoint: s.URL, HTTPClient: hc, MaxRetries: -1, MaxEventSize: cfg.Limits.MaxResponseBytes}
		if s.OAuth != nil {
			key := s.AuthKey
			key.Server = s.Name
			key.URL = s.URL
			key.Identity = scope.Identity
			options := *cloneOAuthOptions(s.OAuth)
			o, err := NewOAuth(life, key, options)
			if err != nil {
				c.Close()
				return nil, err
			}
			c.oauth = o
			// The SDK detaches token requests from their caller's context. Stop
			// refreshes with connection life, including during session setup,
			// but retain valid cached tokens for authenticated session DELETE.
			o.refreshLife = life
			c.authState = o.State()
			o.onPublish = c.authChanged
			if options.OnChange != nil {
				o.onPublish = func(state OAuthState) {
					// Fence before the OAuth gate exposes the published token,
					// including concurrent requests already preparing dispatch.
					c.authPending.Add(1)
					c.authChanged(state)
				}
				o.onChange = func(state OAuthState) { c.notifyAuthChanged(options.OnChange, state) }
			}
			t.OAuthHandler = o.TokenOnlyHandler()
		}
		transport = t
	} else {
		p, err := startManagedProcess(life, s, cfg.Limits.MaxStderrBytes)
		if err != nil {
			c.Close()
			return nil, err
		}
		c.process = p
		transport = &sdk.IOTransport{Reader: c.observer.Reader(p.stdout), Writer: c.observer.Writer(p.stdin), MaxLineLength: cfg.Limits.MaxResponseBytes}
	}
	setup, stop := context.WithTimeout(life, s.Timeout)
	defer stop()
	session, err := client.Connect(setup, transport, nil)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("mcp: connect %s: %w", s.Name, err)
	}
	c.mu.Lock()
	if c.closed.Load() || c.stale.Load() {
		c.mu.Unlock()
		_ = session.Close()
		return nil, ErrStale
	}
	c.session = session
	c.mu.Unlock()
	go func() {
		c.sessionErr = session.Wait()
		close(c.sessionDone)
		_ = c.Close()
	}()
	if c.process != nil {
		go func() {
			<-c.process.done
			_ = c.Close()
		}()
	}
	return c, nil
}

func (c *Connection) Scope() Scope       { return c.scope }
func (c *Connection) Generation() uint64 { return c.generation }

// IsCurrent verifies a previously frozen catalog against this live connection.
// It performs no I/O and serializes with notification-driven invalidation.
func (c *Connection) IsCurrent(catalog Catalog) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.available() == nil && c.catalogReads == 0 && catalog.Server == c.config.Name && catalog.Scope == c.scope && catalog.Generation == c.generation && catalog.Revision == c.revision.Load() && c.catalog != nil && c.catalog.SourceDigest == catalog.SourceDigest
}
func (c *Connection) Config() ServerConfig {
	return cloneServerConfig(c.config)
}

func (c *Connection) available() error {
	if c.stale.Load() {
		return ErrStale
	}
	var processEnded bool
	var processErr error
	if c.process != nil {
		select {
		case <-c.process.done:
			processEnded, processErr = true, c.process.waitErr
		default:
		}
	}
	select {
	case <-c.sessionDone:
		return errors.Join(ErrClosed, processErr, c.sessionErr)
	default:
	}
	if processEnded {
		return errors.Join(ErrClosed, processErr)
	}
	if c.closed.Load() {
		return ErrClosed
	}
	if err := c.ctx.Err(); err != nil {
		return err
	}
	if c.authPending.Load() != 0 {
		return ErrNotReady
	}
	return nil
}

func (c *Connection) invalidate() {
	c.mu.Lock()
	c.revision.Add(1)
	c.mu.Unlock()
}

func (c *Connection) authChanged(state OAuthState) {
	c.authMu.Lock()
	if state.CredentialVersion < c.authState.CredentialVersion {
		c.authMu.Unlock()
		return
	}
	oldScopes := append([]string(nil), c.authState.GrantedScopes...)
	newScopes := append([]string(nil), state.GrantedScopes...)
	slices.Sort(oldScopes)
	slices.Sort(newScopes)
	changed := state.Authenticated != c.authState.Authenticated || !slices.Equal(oldScopes, newScopes)
	c.authState = state
	c.authState.GrantedScopes = newScopes
	retire := changed && c.stale.CompareAndSwap(false, true)
	c.authMu.Unlock()
	if retire {
		c.cancel()
		// A token callback can run inside an SDK request. Do not wait for that
		// same request's teardown while holding its token-source call stack.
		go func() { _ = c.Close() }()
	}
}

func (c *Connection) notifyAuthChanged(notify func(OAuthState), state OAuthState) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer c.authPending.Add(-1)
		returned := false
		defer func() {
			if !returned {
				// The callback cannot propagate a panic across this handoff.
				// Fail closed before releasing its dispatch fence instead of
				// crashing the process or allowing incomplete host policy.
				_ = recover()
				c.stale.Store(true)
				c.cancel()
				go func() { _ = c.Close() }()
			}
		}()
		notify(state)
		returned = true
	}()
	// The SDK calls Token with its session mutex held. A host callback may
	// cancel this connection and synchronously wait for Close/SetScope, which
	// needs that mutex. Cancellation releases this token call while the dispatch
	// fence still rejects the request; otherwise the callback finishes first.
	select {
	case <-done:
	case <-c.ctx.Done():
	}
}

func (c *Connection) requestContext(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := c.available(); err != nil {
		return nil, nil, err
	}
	r, cancel := context.WithTimeout(ctx, c.config.Timeout)
	r = WithDispatchCheck(r, func(context.Context) error { return c.available() })
	stop := context.AfterFunc(c.ctx, cancel)
	return r, func() { stop(); cancel() }, nil
}

func (c *Connection) raw(result sdk.Result) (json.RawMessage, error) {
	raw, err := c.observer.Raw(result)
	if err != nil {
		return nil, err
	}
	if len(raw) > c.limits.MaxResponseBytes {
		return nil, errors.New("mcp: response limit exceeded")
	}
	if err := jsontext.ValidateUnicode(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func decode(raw []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(value); err != nil {
		return err
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("mcp: multiple JSON values")
	}
	return nil
}

// ListTools preserves schemas from the associated true wire page, even when
// the SDK returns a cached page. No typed serialization is used as a fallback.
func (c *Connection) ListTools(ctx context.Context, p *sdk.ListToolsParams) (out *sdk.ListToolsResult, err error) {
	cursor := ""
	if p != nil {
		cursor = p.Cursor
	}
	var dispatched atomic.Bool
	var source *rawBinding
	ctx = WithDispatchCheck(ctx, func(context.Context) error {
		// Cache hits never reach the byte boundary. Fence a real refresh until
		// its bytes are checked; unchanged legacy schema re-lists stay valid.
		if dispatched.CompareAndSwap(false, true) {
			c.mu.Lock()
			c.catalogReads++
			c.mu.Unlock()
		}
		return nil
	})
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if dispatched.Load() {
			c.catalogReads--
		}
		if c.catalog == nil {
			if dispatched.Load() {
				c.revision.Add(1)
			}
			return
		}
		if c.catalog.Revision != c.revision.Load() {
			return
		}
		if err != nil {
			if dispatched.Load() {
				c.revision.Add(1)
			}
			return
		}
		old := c.catalog.page(cursor)
		if old != nil && old.source == source {
			return
		}
		if old == nil || !bytes.Equal(old.source.raw, source.raw) {
			c.revision.Add(1)
			return
		}
		// Revalidate a genuinely new observation once with direct equality,
		// then retain its identity without decoding/hashing the catalog again.
		page := *old
		page.source = source
		frozen := *c.catalog
		frozen.pages = slices.Clone(frozen.pages)
		for i, existing := range frozen.pages {
			if existing == old {
				frozen.pages[i] = &page
			}
		}
		c.catalog = &frozen
	}()
	result, observed, err := c.listToolsPage(ctx, p)
	source = observed
	if err != nil {
		return nil, err
	}
	page, err := c.freezeToolPage(cursor, result, source)
	if err != nil {
		return nil, err
	}
	return &page.result, nil
}

func (c *Connection) listToolsPage(ctx context.Context, p *sdk.ListToolsParams) (*sdk.ListToolsResult, *rawBinding, error) {
	r, stop, err := c.requestContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer stop()
	result, err := c.session.ListTools(r, p)
	if err != nil {
		return nil, nil, err
	}
	source, err := c.observer.binding(result)
	if err != nil {
		return nil, nil, err
	}
	if err := c.available(); err != nil {
		return nil, nil, err
	}
	return result, source, nil
}

func (c *Connection) freezeToolPage(cursor string, result *sdk.ListToolsResult, source *rawBinding) (*catalogPage, error) {
	raw := source.raw
	if len(raw) > c.limits.MaxResponseBytes {
		return nil, errors.New("mcp: response limit exceeded")
	}
	if err := jsontext.ValidateUnicode(raw); err != nil {
		return nil, err
	}
	var out sdk.ListToolsResult
	if err := decode(raw, &out); err != nil {
		return nil, err
	}
	// The SDK can reject tools with invalid protocol annotations. Raw number
	// restoration must not resurrect a capability that the SDK discarded.
	if len(out.Tools) != len(result.Tools) {
		return nil, errors.New("mcp: SDK rejected a raw directory entry")
	}
	for i, tool := range out.Tools {
		if tool == nil || result.Tools[i] == nil || tool.Name != result.Tools[i].Name {
			return nil, errors.New("mcp: raw directory does not match SDK result")
		}
	}
	var page struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(raw, &page); err != nil || len(page.Tools) != len(out.Tools) {
		return nil, errors.New("mcp: malformed tool directory")
	}
	for i, t := range out.Tools {
		if t == nil {
			return nil, errors.New("mcp: nil tool")
		}
		var schemas struct {
			Input  json.RawMessage `json:"inputSchema"`
			Output json.RawMessage `json:"outputSchema"`
		}
		if err := json.Unmarshal(page.Tools[i], &schemas); err != nil {
			return nil, err
		}
		t.InputSchema = schemas.Input
		if len(schemas.Output) > 0 {
			t.OutputSchema = schemas.Output
		}
	}
	if err := c.available(); err != nil {
		return nil, err
	}
	return &catalogPage{cursor: cursor, source: source, result: out, tools: page.Tools}, nil
}

func (c *Connection) CallTool(ctx context.Context, p *sdk.CallToolParams) (*sdk.CallToolResult, error) {
	result, _, err := c.CallToolTracked(ctx, p)
	return result, err
}

func (c *Connection) CallToolTracked(ctx context.Context, p *sdk.CallToolParams) (outResult *sdk.CallToolResult, record DispatchRecord, callErr error) {
	ctx, snapshot := c.observer.Track(ctx)
	defer func() { record = snapshot() }()
	if p == nil || c.config.ToolExposure(p.Name) == Hidden {
		return nil, record, ErrHidden
	}
	r, stop, err := c.requestContext(ctx)
	if err != nil {
		return nil, record, err
	}
	defer stop()
	result, err := c.session.CallTool(r, p)
	if err != nil {
		if facts := snapshot(); facts.ResultType != "" {
			return nil, record, &ResponseError{NeedsInput: facts.ResultType == "input_required", Err: err}
		}
		return nil, record, err
	}
	defer c.observer.Forget(result)
	if result == nil {
		return nil, record, errors.New("mcp: nil tool response")
	}
	raw, err := c.raw(result)
	if err != nil {
		return nil, record, &ResponseError{NeedsInput: result.NeedsInput(), Err: err}
	}
	// Copy SDK lifecycle fields (including input_required) instead of claiming
	// every raw response was complete. Restore only generic JSON from wire.
	out := *result
	var fields map[string]json.RawMessage
	if err := decode(raw, &fields); err != nil {
		return nil, record, &ResponseError{NeedsInput: result.NeedsInput(), Err: err}
	}
	if value, ok := fields["structuredContent"]; ok {
		if err := decode(value, &out.StructuredContent); err != nil {
			return nil, record, &ResponseError{NeedsInput: result.NeedsInput(), Err: err}
		}
	}
	if err := c.available(); err != nil {
		return nil, record, &ResponseError{NeedsInput: result.NeedsInput(), Err: err}
	}
	return &out, record, nil
}

func (c *Connection) Refresh(ctx context.Context) (Catalog, error) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	if err := c.available(); err != nil {
		return Catalog{}, err
	}
	rev := c.revision.Load()
	c.mu.RLock()
	cached := c.catalog
	reading := c.catalogReads != 0
	c.mu.RUnlock()
	if reading {
		return Catalog{}, ErrStale
	}
	init := c.session.InitializeResult()
	snap := Catalog{Server: c.config.Name, Scope: c.scope, Generation: c.generation, Revision: rev, Description: c.config.Description}
	if init != nil {
		snap.Protocol = init.ProtocolVersion
		snap.Instructions = init.Instructions
		if init.Capabilities != nil {
			snap.Resources = init.Capabilities.Resources != nil
		}
	}
	toolsSupported := init != nil && init.Capabilities != nil && init.Capabilities.Tools != nil
	seen := map[string]bool{}
	names := map[string]bool{}
	var pages []*catalogPage
	cursor := ""
	total := 0
	unchanged := cached != nil
	for page := 0; toolsSupported && page < c.limits.MaxPages; page++ {
		// Always ask the SDK: it owns cache notification and TTL invalidation.
		// A cached SDK object keeps its original, genuinely observed raw page.
		res, source, err := c.listToolsPage(ctx, &sdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			return Catalog{}, err
		}
		frozen := cached.page(cursor)
		if frozen == nil || frozen.source != source {
			unchanged = false
			frozen, err = c.freezeToolPage(cursor, res, source)
			if err != nil {
				return Catalog{}, err
			}
		}
		pages = append(pages, frozen)
		total += len(source.raw)
		if total > c.limits.MaxCatalogBytes {
			return Catalog{}, errors.New("mcp: catalog limit exceeded")
		}
		for _, t := range frozen.result.Tools {
			if t == nil || t.Name == "" || names[t.Name] {
				return Catalog{}, errors.New("mcp: invalid or duplicate tool")
			}
			names[t.Name] = true
			if len(snap.Tools) >= c.limits.MaxTools {
				return Catalog{}, errors.New("mcp: catalog limit exceeded")
			}
			snap.Tools = append(snap.Tools, t)
		}
		cursor = frozen.result.NextCursor
		if cursor == "" {
			break
		}
		if seen[cursor] {
			return Catalog{}, errors.New("mcp: repeated directory cursor")
		}
		seen[cursor] = true
		if page == c.limits.MaxPages-1 {
			return Catalog{}, errors.New("mcp: directory page limit exceeded")
		}
	}
	unchanged = unchanged && len(pages) == len(cached.pages)
	if unchanged {
		snap.SourceDigest = cached.SourceDigest
	} else {
		// The public digest describes true wire bytes. Compute it once for a
		// new frozen page set, never to recognize an SDK cache hit.
		digest := sha256.New()
		for _, page := range pages {
			fmt.Fprintf(digest, "%d:", len(page.source.raw))
			digest.Write(page.source.raw)
		}
		snap.SourceDigest = hex.EncodeToString(digest.Sum(nil))
	}
	if c.available() != nil || ctx.Err() != nil || c.revision.Load() != rev {
		return Catalog{}, ErrStale
	}
	if cached != nil && cached.Revision == rev && cached.SourceDigest != snap.SourceDigest {
		if !c.revision.CompareAndSwap(rev, rev+1) {
			return Catalog{}, ErrStale
		}
		rev++
		snap.Revision = rev
	}
	c.mu.Lock()
	if c.available() != nil || c.catalogReads != 0 || c.revision.Load() != rev {
		c.mu.Unlock()
		return Catalog{}, ErrStale
	}
	frozen := &frozenCatalog{Catalog: snap, pages: pages}
	if unchanged && cached.Revision == rev {
		frozen = cached
	}
	c.catalog = frozen
	c.mu.Unlock()
	return cloneCatalog(frozen)
}

func cloneCatalog(frozen *frozenCatalog) (Catalog, error) {
	c := frozen.Catalog
	c.Tools = make([]*sdk.Tool, 0, len(c.Tools))
	for _, page := range frozen.pages {
		for i, raw := range page.tools {
			var copied sdk.Tool
			if err := decode(raw, &copied); err != nil {
				return Catalog{}, err
			}
			t := page.result.Tools[i]
			if v, ok := t.InputSchema.(json.RawMessage); ok {
				copied.InputSchema = append(json.RawMessage(nil), v...)
			}
			if v, ok := t.OutputSchema.(json.RawMessage); ok {
				copied.OutputSchema = append(json.RawMessage(nil), v...)
			}
			c.Tools = append(c.Tools, &copied)
		}
	}
	return c, nil
}

func (c *Connection) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		c.cancel()
		c.mu.RLock()
		session := c.session
		c.mu.RUnlock()
		var errs []error
		if session != nil {
			err := session.Close()
			// The SDK skips DELETE when a retired OAuth source refuses to start
			// a refresh. Only that exact pre-claim outcome is normal teardown;
			// retain interrupted refreshes and actual DELETE/transport failures,
			// including context cancellation from the host's HTTP client.
			if _, skipped := err.(*authRetiredRefreshError); !skipped {
				errs = append(errs, err)
			}
		}
		if c.process != nil {
			errs = append(errs, c.process.close())
		}
		if c.oauth != nil {
			errs = append(errs, c.oauth.Close())
		}
		c.observer.Close()
		c.closeErr = errors.Join(errs...)
		close(c.closeDone)
	})
	<-c.closeDone
	return c.closeErr
}

// resource calls share the same identity, raw bounds and cancellation contract.
func (c *Connection) resourceAllowed() error {
	if err := c.available(); err != nil {
		return err
	}
	if c.config.Exposure == Hidden {
		return ErrHidden
	}
	return nil
}
func (c *Connection) ListResources(ctx context.Context, p *sdk.ListResourcesParams) (*sdk.ListResourcesResult, error) {
	if err := c.resourceAllowed(); err != nil {
		return nil, err
	}
	r, stop, err := c.requestContext(ctx)
	if err != nil {
		return nil, err
	}
	defer stop()
	result, err := c.session.ListResources(r, p)
	if err != nil {
		return nil, err
	}
	raw, err := c.raw(result)
	if err != nil {
		return nil, &ResponseError{Err: err}
	}
	var out sdk.ListResourcesResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &ResponseError{Err: err}
	}
	if err := c.available(); err != nil {
		return nil, err
	}
	return &out, nil
}
func (c *Connection) ListResourceTemplates(ctx context.Context, p *sdk.ListResourceTemplatesParams) (*sdk.ListResourceTemplatesResult, error) {
	if err := c.resourceAllowed(); err != nil {
		return nil, err
	}
	r, stop, err := c.requestContext(ctx)
	if err != nil {
		return nil, err
	}
	defer stop()
	result, err := c.session.ListResourceTemplates(r, p)
	if err != nil {
		return nil, err
	}
	raw, err := c.raw(result)
	if err != nil {
		return nil, &ResponseError{Err: err}
	}
	var out sdk.ListResourceTemplatesResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &ResponseError{Err: err}
	}
	if err := c.available(); err != nil {
		return nil, err
	}
	return &out, nil
}
func (c *Connection) ReadResource(ctx context.Context, p *sdk.ReadResourceParams) (*sdk.ReadResourceResult, error) {
	if err := c.resourceAllowed(); err != nil {
		return nil, err
	}
	r, stop, err := c.requestContext(ctx)
	if err != nil {
		return nil, err
	}
	defer stop()
	result, err := c.session.ReadResource(r, p)
	if err != nil {
		return nil, err
	}
	raw, err := c.raw(result)
	if err != nil {
		return nil, &ResponseError{NeedsInput: result.NeedsInput(), Err: err}
	}
	// Re-decode the observed response with the SDK's decoder so all mutable
	// content, metadata and input requests are detached from its cached result.
	var out sdk.ReadResourceResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &ResponseError{NeedsInput: result.NeedsInput(), Err: err}
	}
	if err := c.available(); err != nil {
		return nil, err
	}
	return &out, nil
}
