package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

var (
	ErrAuthRequired       = errors.New("MCP authentication required")
	ErrOAuthClosed        = errors.New("MCP OAuth is closed")
	ErrOAuthConfiguration = errors.New("invalid MCP OAuth configuration")
	errOAuthAuthorization = errors.New("MCP OAuth authorization failed")
)

// AuthRequiredError reports an authentication challenge without exposing its
// headers, response body, request URL or credentials. It never authorizes replay.
type AuthRequiredError struct {
	StatusCode int
}

func (*AuthRequiredError) Error() string { return ErrAuthRequired.Error() }
func (*AuthRequiredError) Unwrap() error { return ErrAuthRequired }

// OAuthRefreshError hides token endpoint response bodies and marks a refresh as
// requiring explicit authentication. An ambiguous refresh is never repeated.
type OAuthRefreshError struct{ cause error }

func (*OAuthRefreshError) Error() string {
	return "MCP OAuth token refresh failed; explicit authentication required"
}
func (e *OAuthRefreshError) Unwrap() error      { return e.cause }
func (*OAuthRefreshError) Is(target error) bool { return target == ErrAuthRequired }

// OAuthState is safe for host lifecycle decisions; it contains no credentials.
// CredentialVersion changes after login, clear, and each durable refresh claim
// or completion. A successful refresh advances the version twice.
type OAuthState struct {
	Authenticated     bool
	CredentialVersion uint64
	GrantedScopes     []string
}

// OAuthOptions leaves presentation, browser callbacks, server trust and secure
// storage to the host. AuthorizationCodeFetcher must return code, state and iss
// unchanged from the actual redirect. No browser or callback server is started.
//
// SDKConfig.Client, registration, redirect, ScopeFilter, RequestRefreshToken and
// AcceptUnadvertisedIss are honored. InitialTokenSource and NewTokenSource are
// owned by this adapter and must be nil. Configuration is copied on construction.
type OAuthOptions struct {
	SDKConfig auth.AuthorizationCodeHandlerConfig
	// MetadataURL explicitly overrides authorization-server metadata discovery
	// for the configured issuer. Real response bytes still pass SDK validation.
	MetadataURL string
	// Store defaults to a process-local MemoryCredentialStore.
	Store CredentialStore
	// OnChange is called after publication with a secret-free snapshot and no
	// internal lock held. The host can invalidate or rebuild its MCP session.
	// A connection fences dispatch until the callback returns or the session is
	// canceled. On cancellation, the callback may finish after the request so it
	// can wait for that same session's teardown without deadlocking the SDK.
	// A panicking connection callback retires the session and rejects dispatch.
	OnChange func(OAuthState)
}

// OAuth manages one server/host-account/issuer/client credential binding. Login
// is explicit; transports receive only TokenOnlyHandler, so a tools/call POST is
// never retransmitted because authentication completed inside Authorize.
type OAuth struct {
	key      AuthKey
	config   auth.AuthorizationCodeHandlerConfig
	client   *http.Client
	metadata string
	store    CredentialStore
	onChange func(OAuthState)
	gate     chan struct{}
	life     context.Context
	cancel   context.CancelFunc
	mu       sync.RWMutex
	snapshot CredentialSnapshot
	invalid  bool
	closed   bool
	// Only the owning Connection installs this hook, before using the source.
	// It fences terminal auth state while mu and gate still hide new tokens;
	// it must not reenter OAuth, invoke host callbacks, or wait for Close.
	onPublish func(OAuthState)
	// The manager supplies an atomic credential/session publication boundary.
	// Network requests and host callbacks always run outside that boundary.
	commit func(context.Context, uint64, *OAuthCredential) (uint64, error)
}

func NewOAuth(ctx context.Context, key AuthKey, options OAuthOptions) (*OAuth, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config := authCloneConfig(options.SDKConfig)
	if config.InitialTokenSource != nil || config.NewTokenSource != nil || config.AuthorizationCodeFetcher == nil {
		return nil, ErrOAuthConfiguration
	}
	var err error
	key, err = authNormalizeKey(key, config, options.MetadataURL)
	if err != nil {
		return nil, err
	}
	if options.MetadataURL != "" {
		if _, err := authEndpointURL(options.MetadataURL); err != nil {
			return nil, ErrOAuthConfiguration
		}
	}
	if config.PreregisteredClient != nil {
		if config.PreregisteredClient.Issuer != "" && strings.TrimSuffix(config.PreregisteredClient.Issuer, "/") != strings.TrimSuffix(key.Issuer, "/") {
			return nil, ErrOAuthConfiguration
		}
		config.PreregisteredClient.Issuer = key.Issuer
	}
	if _, err := auth.NewAuthorizationCodeHandler(&config); err != nil {
		return nil, ErrOAuthConfiguration
	}
	client := *config.Client
	if client.Timeout == 0 {
		client.Timeout = 30 * time.Second
	}
	// OAuth endpoints must not redirect requests containing client credentials.
	// Hosts can provide their own transport for proxy/SSRF controls; redirects
	// remain an explicit failure rather than credential forwarding or fallback.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if options.Store == nil {
		options.Store = NewMemoryCredentialStore()
	}
	snapshot, err := options.Store.Load(ctx, key)
	if err != nil {
		return nil, authStoreError(err)
	}
	if snapshot.Credential != nil {
		if authValidateCredential(key, snapshot.Credential) != nil || !authConfiguredClient(config, snapshot.Credential.ClientID) {
			return nil, ErrOAuthConfiguration
		}
	}
	snapshot = cloneCredentialSnapshot(snapshot)
	if credential := snapshot.Credential; credential != nil && config.PreregisteredClient != nil && credential.ClientID == config.PreregisteredClient.ClientID {
		// Persisted resolved clients pin identity and registration method, not
		// obsolete host-managed secrets. Honor current preregistration for both
		// code exchanges and refreshes without mutating the store on load. DCR
		// secrets remain owned by the persisted registration.
		credential.ClientSecret = ""
		if secret := config.PreregisteredClient.ClientSecretAuth; secret != nil {
			credential.ClientSecret = secret.ClientSecret
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(context.Background())
	return &OAuth{
		key: key, config: config, client: &client, metadata: options.MetadataURL,
		store: options.Store, onChange: options.OnChange, gate: make(chan struct{}, 1),
		life: life, cancel: cancel, snapshot: snapshot,
	}, nil
}

// Key returns the canonical credential binding. For dynamic or multiple
// configured registration methods, ClientID identifies the configuration; the
// SDK-resolved client is pinned separately in the persisted credential.
func (o *OAuth) Key() AuthKey { return o.key }

func (o *OAuth) State() OAuthState {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.stateLocked()
}

func (o *OAuth) stateLocked() OAuthState {
	state := OAuthState{CredentialVersion: o.snapshot.Version}
	if o.snapshot.Credential != nil && !o.snapshot.Credential.RefreshPending && !o.invalid && !o.closed {
		state.Authenticated = true
		state.GrantedScopes = append([]string(nil), o.snapshot.Credential.Scopes...)
	}
	return state
}

// Authenticate probes only the protected resource with GET and handles its
// challenge using the SDK. It does not send, resume or repeat an MCP POST. The
// host must call this outside tool execution, then connect a new MCP session.
func (o *OAuth) Authenticate(ctx context.Context) error {
	ctx, finish := o.context(ctx)
	defer finish()
	if err := o.acquire(ctx); err != nil {
		return err
	}
	var changed *OAuthState
	defer func() {
		o.release()
		if changed != nil {
			o.notify(*changed)
		}
	}()
	o.mu.RLock()
	previous := cloneCredentialSnapshot(o.snapshot)
	invalid := o.invalid || previous.Credential != nil && previous.Credential.RefreshPending
	o.mu.RUnlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.key.URL, nil)
	if err != nil {
		return ErrOAuthConfiguration
	}
	if previous.Credential != nil && !invalid && previous.Credential.Token.Valid() {
		previous.Credential.Token.SetAuthHeader(req)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return authFlowError(ctx, err)
	}
	resp.Body = &authBoundedBody{Reader: io.LimitReader(resp.Body, 1<<20), Closer: resp.Body}
	if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 && previous.Credential != nil && !invalid {
			return ctx.Err()
		}
		return &AuthRequiredError{StatusCode: resp.StatusCode}
	}
	if resp.StatusCode == http.StatusForbidden {
		challenges, err := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
		if err != nil || !authInsufficientScope(challenges) {
			resp.Body.Close()
			return &AuthRequiredError{StatusCode: resp.StatusCode}
		}
	}
	config := authCloneConfig(o.config)
	boundTransport := &authBoundTransport{base: o.client.Transport, issuer: o.key.Issuer, metadataURL: o.metadata}
	if boundTransport.base == nil {
		boundTransport.base = http.DefaultTransport
	}
	client := *o.client
	client.Transport = boundTransport
	config.Client = &client
	// Reuse the actual SDK-resolved client, including a CIMD fallback. Changed
	// AS capabilities must not select a different client or repeat registration.
	if previous.Credential != nil {
		config.ClientIDMetadataDocumentConfig = nil
		config.DynamicClientRegistrationConfig = nil
		config.PreregisteredClient = &oauthex.ClientCredentials{ClientID: previous.Credential.ClientID, Issuer: o.key.Issuer}
		boundTransport.registeredClient = previous.Credential
		if previous.Credential.ClientSecret != "" {
			config.PreregisteredClient.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: previous.Credential.ClientSecret}
		}
	}
	var requested []string
	filter := config.ScopeFilter
	config.ScopeFilter = func(scopes []string) []string {
		if filter != nil {
			scopes = filter(append([]string(nil), scopes...))
		}
		if previous.Credential != nil {
			scopes = authUnionScopes(previous.Credential.Scopes, scopes)
		}
		requested = append([]string(nil), scopes...)
		return scopes
	}
	fetch := config.AuthorizationCodeFetcher
	config.AuthorizationCodeFetcher = func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
		if !authValidScopes(requested) || !boundTransport.matchesAuthorization(args.URL) {
			return nil, ErrOAuthConfiguration
		}
		result, err := fetch(ctx, args)
		if err != nil {
			return nil, authFlowError(ctx, err)
		}
		if result == nil || result.Code == "" {
			return nil, errOAuthAuthorization
		}
		return result, nil // Preserve state and RFC 9207 iss for SDK validation.
	}
	var staged *OAuthCredential
	config.NewTokenSource = func(_ context.Context, cfg *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !boundTransport.matchesEndpoints(cfg.Endpoint) {
			return nil, ErrOAuthConfiguration
		}
		if !authConfiguredClient(o.config, cfg.ClientID) {
			return nil, ErrOAuthConfiguration
		}
		if previous.Credential != nil && cfg.ClientID != previous.Credential.ClientID {
			return nil, ErrOAuthConfiguration
		}
		scopes := append([]string(nil), cfg.Scopes...)
		if granted, ok := token.Extra("scope").(string); ok {
			scopes = strings.Fields(granted) // An explicit empty scope grants none.
		}
		staged = &OAuthCredential{
			Binding: o.key, ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret,
			Endpoint: cfg.Endpoint, Resource: boundTransport.resource, RedirectURL: cfg.RedirectURL,
			Scopes: authUnionScopes(nil, scopes), Token: token,
		}
		// oauth2.AuthStyleAutoDetect can retry a failed refresh POST using a
		// second client-auth style. Persist the style actually used for the
		// successful code exchange so refresh never performs that retry.
		staged.Endpoint.AuthStyle = boundTransport.tokenStyle
		if err := authValidateCredential(o.key, staged); err != nil {
			return nil, err
		}
		return oauth2.StaticTokenSource(token), nil
	}
	handler, err := auth.NewAuthorizationCodeHandler(&config)
	if err != nil {
		resp.Body.Close()
		return ErrOAuthConfiguration
	}
	if err := handler.Authorize(ctx, req, resp); err != nil {
		return authFlowError(ctx, err)
	}
	if staged == nil {
		return errOAuthAuthorization
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	version, err := o.commitCredential(ctx, previous.Version, staged)
	if version != 0 {
		state := o.publishSnapshot(version, staged)
		changed = &state
	}
	if err != nil {
		return authStoreError(err)
	}
	return nil
}

// TokenOnlyHandler provides access tokens and rejects every challenge. Even a
// read-only MCP POST is not automatically replayed: authentication is explicit.
func (o *OAuth) TokenOnlyHandler() auth.OAuthHandler { return authTokenOnlyHandler{o} }

func (o *OAuth) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.closed {
		return nil, ErrOAuthClosed
	}
	if o.snapshot.Credential == nil || o.snapshot.Credential.RefreshPending || o.invalid {
		return nil, ErrAuthRequired
	}
	return authContextTokenSource{o: o, ctx: ctx}, nil
}

type authTokenOnlyHandler struct{ o *OAuth }

func (h authTokenOnlyHandler) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	return h.o.TokenSource(ctx)
}

func (authTokenOnlyHandler) Authorize(_ context.Context, _ *http.Request, resp *http.Response) error {
	status := 0
	if resp != nil {
		status = resp.StatusCode
		if resp.Body != nil {
			resp.Body.Close()
		}
	}
	return &AuthRequiredError{StatusCode: status}
}

type authContextTokenSource struct {
	o   *OAuth
	ctx context.Context
}

func (s authContextTokenSource) Token() (*oauth2.Token, error) { return s.o.token(s.ctx) }

func (o *OAuth) token(ctx context.Context) (*oauth2.Token, error) {
	ctx, finish := o.context(ctx)
	defer finish()
	if err := o.acquire(ctx); err != nil {
		return nil, err
	}
	var changed *OAuthState
	defer func() {
		o.release()
		if changed != nil {
			o.notify(*changed)
		}
	}()
	o.mu.RLock()
	previous := cloneCredentialSnapshot(o.snapshot)
	invalid := o.invalid
	o.mu.RUnlock()
	if previous.Credential == nil || previous.Credential.RefreshPending || invalid {
		return nil, ErrAuthRequired
	}
	credential := previous.Credential
	if credential.Token.Valid() {
		return credential.Token, nil
	}
	if credential.Token.RefreshToken == "" {
		state := o.invalidate()
		changed = &state
		return nil, ErrAuthRequired
	}
	// Claim before any physical refresh POST. CAS makes independent OAuth
	// objects and processes contend before sending, rather than discovering a
	// conflict only after both have consumed the same remote refresh token.
	credential.RefreshPending = true
	claimVersion, err := o.store.CompareAndSwap(ctx, o.key, previous.Version, credential)
	if claimVersion != 0 {
		o.publishRefreshClaim(claimVersion, credential)
	}
	if err != nil {
		state := o.invalidate()
		changed = &state
		return nil, authStoreError(err)
	}
	cfg := &oauth2.Config{
		ClientID: credential.ClientID, ClientSecret: credential.ClientSecret,
		Endpoint: credential.Endpoint, RedirectURL: credential.RedirectURL,
		Scopes: append([]string(nil), credential.Scopes...),
	}
	refreshClient := *o.client
	base := refreshClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	refreshClient.Transport = authRefreshTransport{base: base, endpoint: credential.Endpoint.TokenURL, resource: credential.Resource}
	refreshCtx := context.WithValue(ctx, oauth2.HTTPClient, &refreshClient)
	token, err := cfg.TokenSource(refreshCtx, credential.Token).Token()
	if err != nil {
		// A refresh may have rotated its token remotely. Do not retry it after an
		// ambiguous failure or cancellation, even if the stored token is unchanged.
		state := o.invalidate()
		changed = &state
		if ctx.Err() != nil {
			err = errors.Join(ctx.Err(), err)
		}
		return nil, &OAuthRefreshError{cause: err}
	}
	if err := ctx.Err(); err != nil {
		state := o.invalidate()
		changed = &state
		return nil, &OAuthRefreshError{cause: err}
	}
	credential.Token = token
	credential.RefreshPending = false
	if granted, ok := token.Extra("scope").(string); ok {
		credential.Scopes = authUnionScopes(nil, strings.Fields(granted))
	}
	if err := authValidateCredential(o.key, credential); err != nil {
		state := o.invalidate()
		changed = &state
		return nil, &OAuthRefreshError{cause: err}
	}
	version, err := o.store.CompareAndSwap(ctx, o.key, claimVersion, credential)
	if version != 0 {
		state := o.publishSnapshot(version, credential)
		changed = &state
	}
	if err != nil {
		if version == 0 {
			state := o.invalidate()
			changed = &state
		}
		return nil, authStoreError(err)
	}
	return cloneOAuthCredential(credential).Token, nil
}

func (o *OAuth) publishSnapshot(version uint64, credential *OAuthCredential) OAuthState {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.snapshot = CredentialSnapshot{Version: version, Credential: cloneOAuthCredential(credential)}
	o.invalid = false
	state := o.stateLocked()
	if o.onPublish != nil {
		o.onPublish(state)
	}
	return state
}

// A pending claim blocks refresh reuse without retiring a same-scope session.
// Its terminal success or failure performs the connection publication fence.
func (o *OAuth) publishRefreshClaim(version uint64, credential *OAuthCredential) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.snapshot = CredentialSnapshot{Version: version, Credential: cloneOAuthCredential(credential)}
	o.invalid = false
}

func (o *OAuth) invalidate() OAuthState {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.invalid = true
	state := o.stateLocked()
	if o.onPublish != nil {
		o.onPublish(state)
	}
	return state
}

// Clear atomically tombstones this binding. Other server/account/client entries
// remain intact. It does not revoke tokens at the remote authorization server.
func (o *OAuth) Clear(ctx context.Context) error {
	ctx, finish := o.context(ctx)
	defer finish()
	if err := o.acquire(ctx); err != nil {
		return err
	}
	var changed *OAuthState
	defer func() {
		o.release()
		if changed != nil {
			o.notify(*changed)
		}
	}()
	o.mu.RLock()
	previous := o.snapshot.Version
	o.mu.RUnlock()
	version, err := o.commitCredential(ctx, previous, nil)
	if version != 0 {
		state := o.publishSnapshot(version, nil)
		changed = &state
	}
	if err != nil {
		return authStoreError(err)
	}
	return nil
}

func (o *OAuth) commitCredential(ctx context.Context, version uint64, credential *OAuthCredential) (uint64, error) {
	if o.commit != nil {
		return o.commit(ctx, version, credential)
	}
	return o.store.CompareAndSwap(ctx, o.key, version, credential)
}

func (o *OAuth) Close() error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.closed = true
	o.mu.Unlock()
	o.cancel()
	return nil
}

func (o *OAuth) notify(state OAuthState) {
	if o.onChange != nil {
		o.onChange(state)
	}
}

func (o *OAuth) context(ctx context.Context) (context.Context, func()) {
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(o.life, cancel)
	return child, func() { stop(); cancel() }
}

func (o *OAuth) acquire(ctx context.Context) error {
	select {
	case o.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	o.mu.RLock()
	closed := o.closed
	o.mu.RUnlock()
	if closed || ctx.Err() != nil {
		o.release()
		if closed {
			return ErrOAuthClosed
		}
		return ctx.Err()
	}
	return nil
}

func (o *OAuth) release() { <-o.gate }

func authNormalizeKey(key AuthKey, config auth.AuthorizationCodeHandlerConfig, metadataURL string) (AuthKey, error) {
	if err := authValidateKeyFields(key, true); err != nil {
		return key, err
	}
	u, err := authEndpointURL(key.URL)
	if err != nil || u.RawQuery != "" {
		return key, ErrOAuthConfiguration
	}
	key.URL = authCanonicalURL(u)
	u, err = authEndpointURL(key.Issuer)
	if err != nil || u.RawQuery != "" {
		return key, ErrOAuthConfiguration
	}
	key.Issuer = authCanonicalURL(u)
	if config.ClientIDMetadataDocumentConfig != nil && !authValidKeyString(config.ClientIDMetadataDocumentConfig.URL, 4096) || config.PreregisteredClient != nil && !authValidKeyString(config.PreregisteredClient.ClientID, 4096) {
		return key, ErrOAuthConfiguration
	}
	client := ""
	methods := 0
	for _, configured := range []bool{config.ClientIDMetadataDocumentConfig != nil, config.PreregisteredClient != nil, config.DynamicClientRegistrationConfig != nil} {
		if configured {
			methods++
		}
	}
	if methods > 1 {
		// Bind every fallback choice, discovery source and redirect to the
		// resource/issuer tuple. The actual selected client remains separate.
		// A host-managed secret is mutable authentication material, not client
		// identity. Retain the authentication method but exclude its value so
		// secret rotation does not orphan the persisted refresh credential.
		preregistered := config.PreregisteredClient
		if preregistered != nil && preregistered.ClientSecretAuth != nil {
			value := *preregistered
			value.ClientSecretAuth = &oauthex.ClientSecretAuth{}
			preregistered = &value
		}
		binding := struct {
			CIMD          *auth.ClientIDMetadataDocumentConfig  `json:"cimd,omitempty"`
			Preregistered *oauthex.ClientCredentials            `json:"preregistered,omitempty"`
			Dynamic       *auth.DynamicClientRegistrationConfig `json:"dynamic,omitempty"`
			RedirectURL   string                                `json:"redirectUrl"`
			MetadataURL   string                                `json:"metadataUrl"`
			Resource      string                                `json:"resource"`
			Issuer        string                                `json:"issuer"`
		}{config.ClientIDMetadataDocumentConfig, preregistered, config.DynamicClientRegistrationConfig, config.RedirectURL, metadataURL, key.URL, key.Issuer}
		data, err := json.Marshal(binding)
		if err != nil {
			return key, ErrOAuthConfiguration
		}
		digest := sha256.Sum256(data)
		client = "registration:" + hex.EncodeToString(digest[:])
	} else if config.ClientIDMetadataDocumentConfig != nil {
		client = config.ClientIDMetadataDocumentConfig.URL
	} else if config.PreregisteredClient != nil {
		client = config.PreregisteredClient.ClientID
	} else if config.DynamicClientRegistrationConfig != nil {
		data, err := json.Marshal(config.DynamicClientRegistrationConfig)
		if err != nil {
			return key, ErrOAuthConfiguration
		}
		digest := sha256.Sum256(data)
		client = "registration:" + hex.EncodeToString(digest[:])
	}
	if key.ClientID == "" {
		key.ClientID = client
	}
	if key.ClientID == "" || key.ClientID != client {
		return key, ErrOAuthConfiguration
	}
	if err := authValidateKeyFields(key, false); err != nil {
		return key, err
	}
	return key, nil
}

// The SDK resolves registration using actual AS metadata. Static client IDs
// must match configured choices; only configured DCR can supply a new ID.
func authConfiguredClient(config auth.AuthorizationCodeHandlerConfig, clientID string) bool {
	if !authValidKeyString(clientID, 4096) {
		return false
	}
	if config.ClientIDMetadataDocumentConfig != nil && clientID == config.ClientIDMetadataDocumentConfig.URL {
		return true
	}
	if config.PreregisteredClient != nil && clientID == config.PreregisteredClient.ClientID {
		return true
	}
	// Preregistration always precedes DCR in the SDK's fallback order.
	return config.PreregisteredClient == nil && config.DynamicClientRegistrationConfig != nil
}

func authEndpointURL(value string) (*url.URL, error) {
	if !authValidKeyString(value, 4096) {
		return nil, ErrOAuthConfiguration
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, ErrOAuthConfiguration
	}
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if !strings.EqualFold(u.Hostname(), "localhost") && (ip == nil || !ip.IsLoopback()) {
			return nil, ErrOAuthConfiguration
		}
	}
	return u, nil
}

func authCanonicalURL(u *url.URL) string {
	copyURL := *u
	copyURL.Scheme = strings.ToLower(copyURL.Scheme)
	copyURL.Host = strings.ToLower(copyURL.Host)
	if (copyURL.Scheme == "https" && copyURL.Port() == "443") || (copyURL.Scheme == "http" && copyURL.Port() == "80") {
		copyURL.Host = copyURL.Hostname()
		if strings.Contains(copyURL.Host, ":") {
			copyURL.Host = "[" + copyURL.Host + "]"
		}
	}
	return copyURL.String()
}

func authValidateCredential(key AuthKey, credential *OAuthCredential) error {
	if credential.Binding != key || credential.ClientID == "" || credential.Token == nil || credential.Token.AccessToken == "" || !authValidScopes(credential.Scopes) {
		return ErrOAuthConfiguration
	}
	if !strings.HasPrefix(key.ClientID, "registration:") && credential.ClientID != key.ClientID {
		return ErrOAuthConfiguration
	}
	if _, err := authEndpointURL(credential.Endpoint.AuthURL); err != nil {
		return err
	}
	if _, err := authEndpointURL(credential.Endpoint.TokenURL); err != nil {
		return err
	}
	resource, err := authEndpointURL(credential.Resource)
	if err != nil || resource.RawQuery != "" {
		return ErrOAuthConfiguration
	}
	server, _ := url.Parse(key.URL)
	root := *server
	root.Path, root.RawPath = "", ""
	if authCanonicalURL(resource) != key.URL && authCanonicalURL(resource) != authCanonicalURL(&root) {
		return ErrOAuthConfiguration
	}
	if credential.Endpoint.AuthStyle != oauth2.AuthStyleInHeader && credential.Endpoint.AuthStyle != oauth2.AuthStyleInParams {
		return ErrOAuthConfiguration
	}
	return nil
}

func authCloneConfig(config auth.AuthorizationCodeHandlerConfig) auth.AuthorizationCodeHandlerConfig {
	if config.ClientIDMetadataDocumentConfig != nil {
		value := *config.ClientIDMetadataDocumentConfig
		config.ClientIDMetadataDocumentConfig = &value
	}
	if config.PreregisteredClient != nil {
		value := *config.PreregisteredClient
		if value.ClientSecretAuth != nil {
			secret := *value.ClientSecretAuth
			value.ClientSecretAuth = &secret
		}
		config.PreregisteredClient = &value
	}
	if config.DynamicClientRegistrationConfig != nil {
		value := *config.DynamicClientRegistrationConfig
		if value.Metadata != nil {
			metadata := *value.Metadata
			metadata.RedirectURIs = append([]string(nil), metadata.RedirectURIs...)
			metadata.GrantTypes = append([]string(nil), metadata.GrantTypes...)
			metadata.ResponseTypes = append([]string(nil), metadata.ResponseTypes...)
			metadata.Contacts = append([]string(nil), metadata.Contacts...)
			value.Metadata = &metadata
		}
		config.DynamicClientRegistrationConfig = &value
	}
	return config
}

func authUnionScopes(existing, requested []string) []string {
	result := append(append([]string(nil), existing...), requested...)
	slices.Sort(result)
	return slices.Compact(result)
}

func authValidScopes(scopes []string) bool {
	for _, scope := range scopes {
		if scope == "" {
			return false
		}
		for _, value := range []byte(scope) {
			if value < 0x21 || value == '"' || value == '\\' || value > 0x7e {
				return false
			}
		}
	}
	return true
}

func authInsufficientScope(challenges []oauthex.Challenge) bool {
	for _, challenge := range challenges {
		if challenge.Scheme == "bearer" && challenge.Params["error"] == "insufficient_scope" {
			return true
		}
	}
	return false
}

func authFlowError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return &authPrivateError{public: ctx.Err(), cause: errors.Join(ctx.Err(), err)}
	}
	for _, public := range []error{context.Canceled, context.DeadlineExceeded, ErrOAuthConfiguration, ErrAuthRequired} {
		if errors.Is(err, public) {
			return &authPrivateError{public: public, cause: err}
		}
	}
	// SDK token endpoint errors may include the response body. Keep that data
	// out of returned error strings and default traces.
	return &authPrivateError{public: errOAuthAuthorization, cause: err}
}

func authStoreError(err error) error {
	for _, public := range []error{context.Canceled, context.DeadlineExceeded, ErrOAuthCredentialConflict, ErrOAuthStoreBusy, ErrStale, ErrClosed} {
		if errors.Is(err, public) {
			return &authPrivateError{public: public, cause: err}
		}
	}
	return &authPrivateError{public: errOAuthStore, cause: err}
}

// Public error strings are safe for ordinary logs and tool projection. The
// original SDK/store/host error remains available to explicit host inspection.
type authPrivateError struct {
	public error
	cause  error
}

func (e *authPrivateError) Error() string        { return e.public.Error() }
func (e *authPrivateError) Unwrap() error        { return e.cause }
func (e *authPrivateError) Is(target error) bool { return errors.Is(e.public, target) }

type authBoundedBody struct {
	io.Reader
	io.Closer
}

// authBoundTransport retains actual metadata bytes. Only discovery GETs for the
// explicitly pinned issuer may be redirected to MetadataURL. Restored client
// requests retain their resolved auth method; MCP requests are not rewritten
// and no requests are retried here.
type authBoundTransport struct {
	base             http.RoundTripper
	issuer           string
	metadataURL      string
	metadata         *oauthex.AuthServerMeta
	tokenStyle       oauth2.AuthStyle
	resource         string
	registeredClient *OAuthCredential
}

func (t *authBoundTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost {
		tokenURL := t.issuer + "/token"
		if t.metadata != nil {
			tokenURL = t.metadata.TokenEndpoint
		}
		if req.URL.String() == tokenURL {
			var err error
			req, t.resource, err = authTokenRequest(req, t.registeredClient, "")
			if err != nil {
				return nil, err
			}
			t.tokenStyle = oauth2.AuthStyleInParams
			if req.Header.Get("Authorization") != "" {
				t.tokenStyle = oauth2.AuthStyleInHeader
			}
		}
	}
	isMetadata := req.Method == http.MethodGet && (strings.Contains(req.URL.Path, "/.well-known/oauth-authorization-server") || strings.Contains(req.URL.Path, "/.well-known/openid-configuration"))
	if !isMetadata {
		return t.base.RoundTrip(req)
	}
	if !slices.Contains(authMetadataURLs(t.issuer), req.URL.String()) {
		return nil, ErrOAuthConfiguration
	}
	if t.metadataURL != "" {
		clone := req.Clone(req.Context())
		clone.URL, _ = url.Parse(t.metadataURL)
		clone.Host = ""
		req = clone
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	resp.Body.Close()
	if err != nil || len(data) > 1<<20 {
		return nil, ErrOAuthConfiguration
	}
	var metadata oauthex.AuthServerMeta
	if json.Unmarshal(data, &metadata) != nil || strings.TrimSuffix(metadata.Issuer, "/") != strings.TrimSuffix(t.issuer, "/") {
		return nil, ErrOAuthConfiguration
	}
	t.metadata = &metadata
	resp.Body = io.NopCloser(bytes.NewReader(data))
	return resp, nil
}

// Keep refresh form generation and response handling in oauth2. The adapter
// supplies the SDK-validated resource identifier, which Config.TokenSource
// cannot express. No token response or metadata is fabricated or replayed.
type authRefreshTransport struct {
	base     http.RoundTripper
	endpoint string
	resource string
}

func (t authRefreshTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost || req.URL.String() != t.endpoint {
		return nil, ErrOAuthConfiguration
	}
	rewritten, _, err := authTokenRequest(req, nil, t.resource)
	if err != nil {
		return nil, err
	}
	return t.base.RoundTrip(rewritten)
}

func authTokenRequest(req *http.Request, registered *OAuthCredential, resource string) (*http.Request, string, error) {
	if req.Body == nil {
		return nil, "", ErrOAuthConfiguration
	}
	data, readErr := io.ReadAll(io.LimitReader(req.Body, (1<<20)+1))
	closeErr := req.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, "", &authPrivateError{public: ErrOAuthConfiguration, cause: err}
	}
	if len(data) > 1<<20 {
		return nil, "", ErrOAuthConfiguration
	}
	form, err := url.ParseQuery(string(data))
	if err != nil {
		return nil, "", &authPrivateError{public: ErrOAuthConfiguration, cause: err}
	}
	if resource != "" {
		if form.Get("grant_type") != "refresh_token" {
			return nil, "", ErrOAuthConfiguration
		}
		form.Set("resource", resource)
	}
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	if registered != nil {
		// The SDK's preregistered-client API cannot select a per-client auth
		// method. Enforce the previously registered method on the real token
		// request while retaining its unchanged authorization-server metadata.
		switch registered.Endpoint.AuthStyle {
		case oauth2.AuthStyleInHeader:
			form.Del("client_id")
			form.Del("client_secret")
			clone.SetBasicAuth(url.QueryEscape(registered.ClientID), url.QueryEscape(registered.ClientSecret))
		case oauth2.AuthStyleInParams:
			clone.Header.Del("Authorization")
			form.Set("client_id", registered.ClientID)
			if registered.ClientSecret != "" {
				form.Set("client_secret", registered.ClientSecret)
			} else {
				form.Del("client_secret")
			}
		default:
			return nil, "", ErrOAuthConfiguration
		}
	}
	body := form.Encode()
	clone.Body = io.NopCloser(strings.NewReader(body))
	clone.ContentLength = int64(len(body))
	clone.GetBody = nil
	return clone, form.Get("resource"), nil
}

func (t *authBoundTransport) matchesAuthorization(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Fragment != "" {
		return false
	}
	endpoint := t.issuer + "/authorize"
	if t.metadata != nil {
		endpoint = t.metadata.AuthorizationEndpoint
	}
	expected, err := authEndpointURL(endpoint)
	if err != nil {
		return false
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false
	}
	fixed, err := url.ParseQuery(expected.RawQuery)
	if err != nil {
		return false
	}
	u.RawQuery, expected.RawQuery = "", ""
	u.ForceQuery, expected.ForceQuery = false, false
	if u.String() != expected.String() {
		return false
	}
	// oauth2 appends its query to the endpoint's existing query. Preserve each
	// fixed value, including repeated values and their order. Metadata must not
	// duplicate an SDK-owned parameter: servers differ on which duplicate wins.
	for name, values := range fixed {
		if authAuthorizationParameter(name) || !slices.Equal(query[name], values) {
			return false
		}
		delete(query, name)
	}
	for name, values := range query {
		if !authAuthorizationParameter(name) || len(values) != 1 || values[0] == "" {
			return false
		}
	}
	// These are the parameters generated by the pinned SDK's authorization-code
	// flow. Scope is optional; state and S256 PKCE remain owned by the SDK.
	return query.Get("response_type") == "code" && query.Get("code_challenge_method") == "S256" &&
		query.Get("client_id") != "" && query.Get("redirect_uri") != "" && query.Get("state") != "" &&
		query.Get("code_challenge") != "" && query.Get("resource") != ""
}

func authAuthorizationParameter(name string) bool {
	switch name {
	case "response_type", "client_id", "redirect_uri", "scope", "state", "code_challenge", "code_challenge_method", "resource":
		return true
	default:
		return false
	}
}

func (t *authBoundTransport) matchesEndpoints(endpoint oauth2.Endpoint) bool {
	if t.metadata != nil {
		return endpoint.AuthURL == t.metadata.AuthorizationEndpoint && endpoint.TokenURL == t.metadata.TokenEndpoint
	}
	return endpoint.AuthURL == t.issuer+"/authorize" && endpoint.TokenURL == t.issuer+"/token"
}

func authMetadataURLs(issuer string) []string {
	u, _ := url.Parse(issuer)
	if u.Path == "" {
		u.Path = "/.well-known/oauth-authorization-server"
		first := u.String()
		u.Path = "/.well-known/openid-configuration"
		return []string{first, u.String()}
	}
	path := u.Path
	u.Path = "/.well-known/oauth-authorization-server/" + strings.TrimLeft(path, "/")
	first := u.String()
	u.Path = "/.well-known/openid-configuration/" + strings.TrimLeft(path, "/")
	second := u.String()
	u.Path = "/" + strings.Trim(path, "/") + "/.well-known/openid-configuration"
	return []string{first, second, u.String()}
}
