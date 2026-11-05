package device

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"

	"github.com/Icatme/pi-go/agent/background"
)

// ClientOptions is host-only configuration. Transport must authenticate the
// intended peer and caller, protect credentials, and MUST NOT retry requests.
// HTTPS is required except for loopback-only protocol fixtures. No login,
// credential loading, environment lookup or transport is supplied implicitly.
type ClientOptions struct {
	URL       string
	Transport http.RoundTripper
	Binding   Binding
	Tools     []string
}

// Client pins a single device grant. There is no automatic device fallback or
// Start retry. After any disconnect inspect the same ID; unknown is review,
// not proof that dispatch is safe. Persist Scope in BackgroundTool.Scope.
type Client struct {
	endpoint string
	http     *http.Client
	binding  Binding
	tools    []string
	scope    string
}

var _ background.Backend = (*Client)(nil)

func NewClient(opts ClientOptions) (*Client, error) {
	if !validConfig(opts.Binding, opts.Tools) || opts.Transport == nil {
		return nil, ErrInvalid
	}
	u, err := url.Parse(opts.URL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, ErrInvalid
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return nil, ErrInvalid
		}
	}
	tools := slices.Clone(opts.Tools)
	slices.Sort(tools)
	return &Client{endpoint: u.String(), http: &http.Client{Transport: opts.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, binding: opts.Binding, tools: tools, scope: Scope(opts.Binding, tools)}, nil
}
func (c *Client) Scope() string { return c.scope }
func (c *Client) Describe(ctx context.Context) (Description, error) {
	r, err := c.exchange(ctx, command{Action: "describe"})
	if err != nil {
		return Description{}, err
	}
	if r.Description == nil || r.Description.Binding != c.binding || !slices.Equal(r.Description.Tools, c.tools) || r.Observation != nil {
		return Description{}, ErrUnavailable
	}
	r.Description.Tools = slices.Clone(r.Description.Tools)
	return *r.Description, nil
}
func (c *Client) Start(ctx context.Context, request background.Request) error {
	if request.Scope != c.scope || !validID(request.ID) || !validID(request.Call.ID) || !slices.Contains(c.tools, request.Call.Name) {
		return ErrInvalid
	}
	r, err := c.exchange(ctx, command{Action: "start", ID: request.ID, Request: &request})
	if err != nil {
		return err
	}
	if !r.Accepted || r.Observation != nil || r.Description != nil {
		return ErrUnavailable
	}
	return nil
}
func (c *Client) Inspect(ctx context.Context, id string) (background.Observation, error) {
	r, err := c.exchange(ctx, command{Action: "inspect", ID: id})
	if err != nil {
		return background.Observation{}, err
	}
	o := r.Observation
	if o == nil || r.Description != nil || o.ID != id || len(o.Output) != 0 || (o.State != "running" && o.State != "unknown" && o.State != "exited") || len(o.Receipts) > 1 {
		return background.Observation{}, ErrUnavailable
	}
	for _, receipt := range o.Receipts {
		if receipt.Token != 1 || receipt.ParentToken != 0 || receipt.Domain != "agent" || !validID(receipt.CallID) || !slices.Contains(c.tools, receipt.ToolName) || receipt.Completed != (o.State == "exited") {
			return background.Observation{}, ErrUnavailable
		}
	}
	return *o, nil
}
func (c *Client) Stop(ctx context.Context, id string) error {
	r, err := c.exchange(ctx, command{Action: "stop", ID: id})
	if err != nil {
		return err
	}
	if r.Observation != nil || r.Description != nil {
		return ErrUnavailable
	}
	return nil
}
func (c *Client) exchange(ctx context.Context, cmd command) (out response, err error) {
	defer func() {
		if recover() != nil {
			out = response{}
			err = ErrUnavailable
		}
	}()
	if ctx == nil {
		return response{}, ErrInvalid
	}
	if ctx.Err() != nil {
		return response{}, ErrUnavailable
	}
	if cmd.Action != "describe" && !validID(cmd.ID) {
		return response{}, ErrInvalid
	}
	cmd.Version = protocolVersion
	cmd.Scope = c.scope
	raw, err := json.Marshal(cmd)
	if err != nil || len(raw) > maxWireBytes {
		return response{}, ErrInvalid
	}
	// The explicit ReadCloser avoids GetBody and ContentLength replay inference.
	// No idempotency header: stock net/http must not retry a mutating POST.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, io.NopCloser(bytes.NewReader(raw)))
	if err != nil {
		return response{}, ErrInvalid
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return response{}, ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		if res.StatusCode == http.StatusForbidden {
			return response{}, ErrDenied
		}
		if res.StatusCode == http.StatusBadRequest {
			return response{}, ErrInvalid
		}
		return response{}, ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxWireBytes+1))
	if err != nil || len(body) > maxWireBytes {
		return response{}, ErrUnavailable
	}
	if strictJSON(body, &out) != nil || out.Version != protocolVersion || out.Scope != c.scope || out.ID != cmd.ID {
		return response{}, ErrUnavailable
	}
	return out, nil
}
