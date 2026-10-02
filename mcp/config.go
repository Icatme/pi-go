// Package mcp manages explicitly configured MCP connections. It does not read
// application configuration, choose identities, or start a model runtime.
package mcp

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

type Exposure string

const (
	Codemode Exposure = "codemode"
	Deferred Exposure = "deferred"
	Direct   Exposure = "direct"
	Hidden   Exposure = "hidden"
)

// Scope is supplied by the trusted application, never by tool arguments.
type Scope struct {
	Identity  string
	AuthEpoch uint64
}

// ToolRule matches exact tool names or '*' patterns. Exact matches win; among
// patterns the first match wins. There are no aliases or implicit approvals.
type ToolRule struct {
	Pattern  string
	Exposure Exposure
}

type ServerConfig struct {
	Name        string
	Description string
	Exposure    Exposure
	ToolRules   []ToolRule
	Disabled    bool
	URL         string
	Headers     http.Header
	Command     string
	Args        []string
	Env         []string
	Dir         string
	// Trusted explicitly authorizes this application-provided process command.
	// On Unix, the command and all owned descendants must stay in the assigned
	// process group. Daemonizing with setsid/setpgid requires host containment.
	Trusted bool
	Timeout time.Duration
	OAuth   *OAuthOptions
	AuthKey AuthKey
}

type Limits struct {
	Wire             WireLimits
	MaxTools         int
	MaxPages         int
	MaxCatalogBytes  int
	MaxResponseBytes int
	MaxStderrBytes   int
}

type Config struct {
	Scope   Scope
	Servers []ServerConfig
	Limits  Limits
	// HTTPClient supplies host network policy. Redirects are always disabled.
	HTTPClient *http.Client
}

var (
	ErrClosed   = errors.New("mcp: manager or connection closed")
	ErrStale    = errors.New("mcp: identity or catalog changed")
	ErrHidden   = errors.New("mcp: tool or resource is unavailable")
	ErrNotReady = errors.New("mcp: server is not connected")
)

func validExposure(e Exposure) bool {
	return e == Codemode || e == Deferred || e == Direct || e == Hidden
}

func normalizeConfig(c Config) (Config, error) {
	if !validIdentity(c.Scope.Identity) {
		return c, errors.New("mcp: trusted identity is required")
	}
	if c.Scope.AuthEpoch == 0 {
		c.Scope.AuthEpoch = 1
	}
	defaults := []int{256, 32, 1 << 20, 16 << 20, 64 << 10}
	fields := []*int{&c.Limits.MaxTools, &c.Limits.MaxPages, &c.Limits.MaxCatalogBytes, &c.Limits.MaxResponseBytes, &c.Limits.MaxStderrBytes}
	for i, p := range fields {
		if *p < 0 || *p > defaults[i] {
			return c, fmt.Errorf("mcp: limit %d exceeds supported bound", i)
		}
		if *p == 0 {
			*p = defaults[i]
		}
	}
	wireDefaults := []int{8 << 20, 64, 256, 32 << 20, 256}
	wireFields := []*int{&c.Limits.Wire.MaxFrameBytes, &c.Limits.Wire.MaxPending, &c.Limits.Wire.MaxBindings, &c.Limits.Wire.MaxRawBytes, &c.Limits.Wire.MaxTombstones}
	for i, p := range wireFields {
		if *p < 0 || *p > wireDefaults[i] {
			return c, errors.New("mcp: wire limit exceeds supported bound")
		}
		if *p == 0 {
			*p = wireDefaults[i]
		}
	}
	if c.HTTPClient != nil {
		client := *c.HTTPClient
		c.HTTPClient = &client
	}
	if len(c.Servers) > 64 {
		return c, errors.New("mcp: server limit exceeded")
	}
	c.Servers = append([]ServerConfig(nil), c.Servers...)
	seen := map[string]bool{}
	for i := range c.Servers {
		s := &c.Servers[i]
		if s.Name == "" || len(s.Name) > 128 || seen[s.Name] {
			return c, errors.New("mcp: invalid or duplicate server name")
		}
		for _, r := range s.Name {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
				return c, errors.New("mcp: invalid server name")
			}
		}
		seen[s.Name] = true
		if s.Exposure == "" {
			s.Exposure = Codemode
		}
		if !validExposure(s.Exposure) {
			return c, errors.New("mcp: invalid exposure")
		}
		if (s.URL == "") == (s.Command == "") {
			return c, fmt.Errorf("mcp: server %s requires exactly one transport", s.Name)
		}
		if s.Command != "" && !s.Trusted {
			return c, fmt.Errorf("mcp: process %s requires host trust", s.Name)
		}
		if s.URL != "" {
			u, err := url.Parse(s.URL)
			if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && loopback(u.Hostname()))) {
				return c, fmt.Errorf("mcp: server %s requires HTTPS or loopback HTTP", s.Name)
			}
		}
		if len(s.Description) > 4096 || len(s.ToolRules) > 256 || len(s.Args) > 256 || len(s.Env) > 256 {
			return c, errors.New("mcp: configuration limit exceeded")
		}
		if s.Timeout < 0 || s.Timeout > 10*time.Minute {
			return c, errors.New("mcp: invalid timeout")
		}
		if s.Timeout == 0 {
			s.Timeout = 60 * time.Second
		}
		s.Args = append([]string(nil), s.Args...)
		s.Env = append([]string(nil), s.Env...)
		s.ToolRules = append([]ToolRule(nil), s.ToolRules...)
		s.Headers = s.Headers.Clone()
		for _, r := range s.ToolRules {
			if r.Pattern == "" || len(r.Pattern) > 256 || !validExposure(r.Exposure) {
				return c, errors.New("mcp: invalid tool exposure rule")
			}
		}
		if s.OAuth != nil && s.URL == "" {
			return c, errors.New("mcp: OAuth requires HTTP")
		}
		if s.OAuth != nil {
			s.OAuth = cloneOAuthOptions(s.OAuth)
			if s.OAuth.Store == nil {
				// Authentication and later connections must use the same store.
				s.OAuth.Store = NewMemoryCredentialStore()
			}
			if s.OAuth.SDKConfig.Client == nil && c.HTTPClient != nil {
				client := *c.HTTPClient
				s.OAuth.SDKConfig.Client = &client
			}
		}
	}
	return c, nil
}

func validIdentity(identity string) bool {
	return strings.TrimSpace(identity) != "" && len(identity) <= 256 && utf8.ValidString(identity) && !strings.ContainsAny(identity, "\x00\r\n")
}

func cloneOAuthOptions(options *OAuthOptions) *OAuthOptions {
	if options == nil {
		return nil
	}
	copy := *options
	copy.SDKConfig = authCloneConfig(options.SDKConfig)
	if copy.SDKConfig.Client != nil {
		client := *copy.SDKConfig.Client
		copy.SDKConfig.Client = &client
	}
	return &copy
}

func cloneServerConfig(s ServerConfig) ServerConfig {
	s.Args = append([]string(nil), s.Args...)
	s.Env = append([]string(nil), s.Env...)
	s.ToolRules = append([]ToolRule(nil), s.ToolRules...)
	s.Headers = s.Headers.Clone()
	s.OAuth = cloneOAuthOptions(s.OAuth)
	return s
}

func loopback(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

func (s ServerConfig) ToolExposure(name string) Exposure {
	for _, r := range s.ToolRules {
		if !strings.Contains(r.Pattern, "*") && r.Pattern == name {
			return r.Exposure
		}
	}
	for _, r := range s.ToolRules {
		if strings.Contains(r.Pattern, "*") && wildcard(r.Pattern, name) {
			return r.Exposure
		}
	}
	return s.Exposure
}

func wildcard(pattern, value string) bool {
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(value, parts[0]) {
		return false
	}
	value = value[len(parts[0]):]
	for i, p := range parts[1:] {
		if i == len(parts)-2 {
			return strings.HasSuffix(value, p)
		}
		at := strings.Index(value, p)
		if at < 0 {
			return false
		}
		value = value[at+len(p):]
	}
	return value == ""
}

func (s ServerConfig) NeedsDirect() bool {
	if s.Exposure == Direct {
		return true
	}
	for _, r := range s.ToolRules {
		if r.Exposure == Direct {
			return true
		}
	}
	return false
}
