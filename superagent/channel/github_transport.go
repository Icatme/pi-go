package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitHubDestination is a host-verified repository path. The host must verify
// that the repository ID, owner ID and pull number still identify this path;
// names alone cannot prove repository identity after transfers or renames.
type GitHubDestination struct{ Owner, Repository string }

// GitHubTransportConfig explicitly authorizes one API origin and exact PR
// destinations, keyed by canonical repositoryID/pull/number. APIBaseURL is an
// HTTPS origin, optionally with /api/v3 for GitHub Enterprise. Numeric loopback
// HTTP is allowed for local tests only. Client.Transport owns authentication;
// this package never discovers, stores or imports credentials. The host must
// trust that RoundTripper to authenticate the configured server and not retry
// POST requests. A client and an explicit non-nil RoundTripper are required.
type GitHubTransportConfig struct {
	Source       string
	OwnerID      string
	APIBaseURL   string
	Destinations map[string]GitHubDestination
	Client       *http.Client
}

// GitHubTransport sends one issue-comment POST per call. It is a trusted
// low-level transport, not an approval API: use Adapter.SendReply to enforce
// durable approval and attempt fencing. It performs no retries or searches.
// Configuration and the client value are copied; the RoundTripper remains
// caller-owned and must be safe for concurrent use.
type GitHubTransport struct {
	source, ownerID, base string
	destinations          map[string]GitHubDestination
	client                http.Client
}

func NewGitHubTransport(c GitHubTransportConfig) (*GitHubTransport, error) {
	bad := errors.New("channel: invalid GitHub transport configuration")
	if ingressDecimalID(json.Number(c.OwnerID)) == "" || c.Source != "github:"+c.OwnerID || c.Client == nil || c.Client.Transport == nil || len(c.Destinations) == 0 {
		return nil, bad
	}
	u, err := url.Parse(c.APIBaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.Opaque != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/api/v3") || u.String() != c.APIBaseURL {
		return nil, bad
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || ip == nil || !ip.IsLoopback() {
			return nil, bad
		}
	}
	tr := &GitHubTransport{source: c.Source, ownerID: c.OwnerID, base: c.APIBaseURL, destinations: make(map[string]GitHubDestination, len(c.Destinations)), client: *c.Client}
	for thread, d := range c.Destinations {
		if !channelThread(GitHub, thread) || !githubPathComponent(d.Owner) || !githubPathComponent(d.Repository) {
			return nil, bad
		}
		tr.destinations[thread] = d
	}
	tr.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	// Cookies are neither needed nor imported for the explicit API transport.
	tr.client.Jar = nil
	if tr.client.Timeout <= 0 || tr.client.Timeout > 30*time.Second {
		tr.client.Timeout = 30 * time.Second
	}
	return tr, nil
}

func githubPathComponent(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 100 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func githubUnknown(r Request) Receipt {
	return Receipt{Source: r.Source, Organization: r.Organization, Thread: r.Thread, Delivery: r.Delivery, Status: "unknown"}
}

func (t *GitHubTransport) request(r Request) (string, string, error) {
	if t == nil || r.Protocol != GitHub || r.Source != t.source || r.Organization != t.ownerID || !channelThread(GitHub, r.Thread) || !channelID(r.Delivery.ReplyID) || r.Delivery.Attempt == 0 || r.IdempotencyKey != r.Delivery.ReplyID {
		return "", "", ErrUncertain
	}
	d, ok := t.destinations[r.Thread]
	if !ok {
		return "", "", ErrUncertain
	}
	root, err := ingressJSON(r.Body)
	if err != nil || len(root) != 1 {
		return "", "", ErrUncertain
	}
	body, ok := root["body"].(string)
	if !ok || strings.TrimSpace(body) == "" || len(body) > 64<<10 {
		return "", "", ErrUncertain
	}
	return t.base + "/repos/" + d.Owner + "/" + d.Repository + "/issues/" + strings.Split(r.Thread, "/")[2], body, nil
}

func (t *GitHubTransport) Send(ctx context.Context, r Request) (Receipt, error) {
	receipt := githubUnknown(r)
	issueURL, body, err := t.request(r)
	if err != nil {
		return receipt, err
	}
	if ctx == nil || ctx.Err() != nil {
		return receipt, ErrUncertain
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, issueURL+"/comments", bytes.NewReader(r.Body))
	if err != nil {
		return receipt, ErrUncertain
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	// GitHub does not promise idempotency for issue-comment creation. In
	// particular, do not turn the durable internal key into an HTTP header.
	response, err := t.client.Do(req)
	if err != nil {
		return receipt, ErrUncertain
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return receipt, ErrUncertain
	}
	root, err := ingressJSON(raw)
	if err != nil {
		return receipt, ErrUncertain
	}
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests {
		if retry := githubRetryAt(response, time.Now()); retry > 0 {
			receipt.Status = "rate_limited"
			receipt.RetryAt = retry
			return receipt, nil
		}
	}
	if response.StatusCode != http.StatusCreated {
		return receipt, ErrUncertain
	}
	id := ingressDecimalID(root["id"])
	commentURL := strings.TrimSuffix(issueURL, "/"+strings.Split(r.Thread, "/")[2]) + "/comments/" + id
	if id == "" || root["body"] != body || root["issue_url"] != issueURL || root["url"] != commentURL {
		return receipt, ErrUncertain
	}
	receipt.Status = "delivered"
	receipt.MessageID = id
	return receipt, nil
}

// Lookup cannot establish absence or authorship by scanning comments. Only
// independently authenticated external evidence can resolve an uncertain send.
func (t *GitHubTransport) Lookup(_ context.Context, r Request) (Receipt, error) {
	return githubUnknown(r), ErrUncertain
}

func githubRetryAt(r *http.Response, now time.Time) int64 {
	const maxMillis int64 = 253402300799999
	var deadline int64
	if r.StatusCode != http.StatusForbidden && r.StatusCode != http.StatusTooManyRequests {
		return 0
	}
	for _, name := range []string{"X-RateLimit-Remaining", "X-RateLimit-Reset", "Retry-After"} {
		if len(r.Header.Values(name)) > 1 {
			return 0
		}
	}
	if value := r.Header.Get("X-RateLimit-Remaining"); value != "" {
		if _, err := strconv.ParseUint(value, 10, 64); err != nil {
			return 0
		}
	}
	// A generic 403 does not prove rate rejection. GitHub marks primary
	// limits with remaining=0, and secondary limits with Retry-After.
	if r.StatusCode == http.StatusForbidden && r.Header.Get("X-RateLimit-Remaining") != "0" && r.Header.Get("Retry-After") == "" {
		return 0
	}
	if value := r.Header.Get("X-RateLimit-Reset"); value != "" {
		n, err := strconv.ParseInt(r.Header.Get("X-RateLimit-Reset"), 10, 64)
		if err != nil || n <= 0 || n > (maxMillis-1000)/1000 {
			return 0
		}
		// Wait through the reset second rather than retrying at its start.
		deadline = n*1000 + 1000
	}
	if value := r.Header.Get("Retry-After"); value != "" {
		var retry int64
		if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
			if seconds > uint64((maxMillis-now.UnixMilli()-1000)/1000) {
				return 0
			}
			retry = now.UnixMilli() + int64(seconds)*1000 + 1000
		} else if date, err := http.ParseTime(value); err == nil {
			retry = date.UnixMilli() + 1000
		} else {
			return 0
		}
		deadline = max(deadline, retry)
	}
	// Missing, invalid or entirely past hints remain unknown. In particular,
	// do not guess a retry deadline for a headerless 429.
	if deadline <= now.UnixMilli() || deadline > maxMillis {
		return 0
	}
	return deadline
}
