package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/oauth2"
)

var (
	// ErrOAuthCredentialConflict means another writer changed the credentials.
	// A caller must not repeat a refresh using the old refresh token.
	ErrOAuthCredentialConflict = errors.New("MCP OAuth credential version conflict")
	ErrOAuthStoreBusy          = errors.New("MCP OAuth credential store is locked")
	errOAuthStore              = errors.New("MCP OAuth credential storage failed")
)

// Keep host diagnostics reachable without putting a path, JSON or credential
// in the public error string. Only the host may choose to inspect the cause.
type authStoreFailure struct {
	operation string
	cause     error
}

func (*authStoreFailure) Error() string        { return errOAuthStore.Error() }
func (e *authStoreFailure) Unwrap() error      { return e.cause }
func (*authStoreFailure) Is(target error) bool { return target == errOAuthStore }

func authStoreCause(operation string, cause error) error {
	return &authStoreFailure{operation: operation, cause: cause}
}

// AuthKey isolates credentials by server configuration, host account, issuer and
// OAuth client configuration. NewOAuth canonicalizes URL and derives ClientID
// when omitted. Multiple registration methods use a configuration digest,
// rather than pretending that the first configured method was selected.
type AuthKey struct {
	Server   string `json:"server"`
	URL      string `json:"url"`
	Identity string `json:"identity"`
	Issuer   string `json:"issuer"`
	ClientID string `json:"clientId"`
}

// OAuthCredential contains secrets. Hosts must protect the credential store and
// must not include this value in logs, tool descriptions, traces or JS results.
type OAuthCredential struct {
	Binding AuthKey `json:"binding"`
	// ClientID is the actual SDK-resolved client, separate from Binding.ClientID.
	ClientID     string          `json:"clientId"`
	ClientSecret string          `json:"clientSecret,omitempty"`
	Endpoint     oauth2.Endpoint `json:"endpoint"`
	Resource     string          `json:"resource"`
	RedirectURL  string          `json:"redirectUrl"`
	Scopes       []string        `json:"scopes"`
	Token        *oauth2.Token   `json:"token"`
	// RefreshPending is durably claimed before sending a refresh token. Only a
	// successful CAS of that result, or explicit Authenticate, can clear it.
	// A restored pending credential must never send its refresh token again.
	RefreshPending bool `json:"refreshPending,omitempty"`
}

// CredentialSnapshot preserves a version even for a deleted entry. A missing
// entry has version zero; a tombstone retains its version to prevent ABA writes.
type CredentialSnapshot struct {
	Version    uint64           `json:"version"`
	Credential *OAuthCredential `json:"credential,omitempty"`
}

// CredentialStore must atomically compare the version and publish the complete
// new value. Passing nil writes a tombstone. Implementations must copy values,
// honor cancellation and keep secrets private. There is no implicit home store.
// CompareAndSwap returns the published next version even if subsequent cleanup
// fails; a zero version with an error means no value was published.
type CredentialStore interface {
	Load(context.Context, AuthKey) (CredentialSnapshot, error)
	CompareAndSwap(context.Context, AuthKey, uint64, *OAuthCredential) (uint64, error)
}

// MemoryCredentialStore is a process-local, concurrent credential store.
type MemoryCredentialStore struct {
	mu      sync.Mutex
	entries map[string]CredentialSnapshot
}

func NewMemoryCredentialStore() *MemoryCredentialStore {
	return &MemoryCredentialStore{entries: make(map[string]CredentialSnapshot)}
}

func (s *MemoryCredentialStore) Load(ctx context.Context, key AuthKey) (CredentialSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return CredentialSnapshot{}, err
	}
	if err := authValidateKeyFields(key, false); err != nil {
		return CredentialSnapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneCredentialSnapshot(s.entries[authKeyID(key)]), nil
}

func (s *MemoryCredentialStore) CompareAndSwap(ctx context.Context, key AuthKey, version uint64, credential *OAuthCredential) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := authValidateKeyFields(key, false); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	id := authKeyID(key)
	current := s.entries[id]
	if current.Version != version || version == math.MaxUint64 {
		return 0, ErrOAuthCredentialConflict
	}
	if s.entries == nil {
		s.entries = make(map[string]CredentialSnapshot)
	}
	s.entries[id] = CredentialSnapshot{Version: version + 1, Credential: cloneOAuthCredential(credential)}
	return version + 1, nil
}

// FileCredentialStore persists a credential map at an explicit absolute path.
// The parent directory must already exist and have host-controlled permissions
// (especially its ACL on Windows). Files use mode 0600 on systems honoring it;
// this is storage, not an encrypted keychain.
//
// All readers and writers use a cross-process exclusive directory lock. A
// crashed process may leave <path>.lock; it is deliberately not removed based
// on a stale-time guess. After checking no writer is alive, the host can remove
// that empty directory. No open lock-file handle crosses acquisition/release.
type FileCredentialStore struct {
	path string
}

func NewFileCredentialStore(path string) (*FileCredentialStore, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("MCP OAuth credential path must be absolute")
	}
	path = filepath.Clean(path)
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, authStoreCause("resolve_parent", err)
	}
	path = filepath.Join(parent, filepath.Base(path))
	if err := authCheckRegularFile(path); err != nil {
		return nil, err
	}
	return &FileCredentialStore{path: path}, nil
}

type authStoreDocument struct {
	Format  int                           `json:"format"`
	Entries map[string]CredentialSnapshot `json:"entries"`
}

func (s *FileCredentialStore) Load(ctx context.Context, key AuthKey) (snapshot CredentialSnapshot, retErr error) {
	if err := ctx.Err(); err != nil {
		return CredentialSnapshot{}, err
	}
	if err := authValidateKeyFields(key, false); err != nil {
		return CredentialSnapshot{}, err
	}
	unlock, err := s.lock(ctx)
	if err != nil {
		return CredentialSnapshot{}, err
	}
	defer func() { retErr = errors.Join(retErr, unlock()) }()
	doc, err := s.read()
	if err != nil {
		return CredentialSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return CredentialSnapshot{}, err
	}
	return cloneCredentialSnapshot(doc.Entries[authKeyID(key)]), nil
}

func (s *FileCredentialStore) CompareAndSwap(ctx context.Context, key AuthKey, version uint64, credential *OAuthCredential) (nextVersion uint64, retErr error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := authValidateKeyFields(key, false); err != nil {
		return 0, err
	}
	unlock, err := s.lock(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { retErr = errors.Join(retErr, unlock()) }()
	doc, err := s.read()
	if err != nil {
		return 0, err
	}
	id := authKeyID(key)
	if doc.Entries[id].Version != version || version == math.MaxUint64 {
		return 0, ErrOAuthCredentialConflict
	}
	doc.Entries[id] = CredentialSnapshot{Version: version + 1, Credential: cloneOAuthCredential(credential)}
	data, err := json.Marshal(doc)
	if err != nil || len(data) > authMaxStoreBytes {
		return 0, errOAuthStore
	}
	if err := s.publish(ctx, data); err != nil {
		return 0, err
	}
	return version + 1, nil
}

const authMaxStoreBytes = 16 << 20

func (s *FileCredentialStore) read() (authStoreDocument, error) {
	doc := authStoreDocument{Format: 1, Entries: make(map[string]CredentialSnapshot)}
	if err := authCheckRegularFile(s.path); err != nil {
		return doc, err
	}
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return doc, nil
	}
	if err != nil {
		return doc, authStoreCause("open", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, authMaxStoreBytes+1))
	if err != nil {
		return doc, authStoreCause("read", err)
	}
	if len(data) > authMaxStoreBytes {
		return doc, errOAuthStore
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return doc, authStoreCause("decode", err)
	}
	if doc.Format != 1 || doc.Entries == nil {
		return doc, errOAuthStore
	}
	return doc, nil
}

func (s *FileCredentialStore) publish(ctx context.Context, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(s.path), ".mcp-oauth-*")
	if err != nil {
		return authStoreCause("create_temp", err)
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return authStoreCause("chmod", err)
	}
	if _, err := f.Write(data); err != nil {
		return authStoreCause("write", err)
	}
	if err := f.Sync(); err != nil {
		return authStoreCause("sync", err)
	}
	if err := f.Close(); err != nil {
		return authStoreCause("close", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(name, s.path); err != nil {
		return authStoreCause("rename", err)
	}
	return nil
}

func (s *FileCredentialStore) lock(ctx context.Context) (func() error, error) {
	// A fixed lock pathname makes the compare/read/write one transaction across
	// independent store objects and processes, rather than just a Go mutex.
	// Mkdir has no persistent file handle. A CREATE_NEW lock file can instead
	// return ERROR_ACCESS_DENIED when another Windows process races its deletion
	// with an open attempt; that is not a credential write failure or an ErrExist.
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	lockPath := s.path + ".lock"
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err := os.Mkdir(lockPath, 0700)
		if err == nil {
			return func() error {
				if err := os.Remove(lockPath); err != nil {
					return authStoreCause("unlock", err)
				}
				return nil
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, authStoreCause("lock", err)
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return nil, ErrOAuthStoreBusy
		case <-timer.C:
		}
	}
}

func authCheckRegularFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return authStoreCause("stat", err)
	}
	if !info.Mode().IsRegular() {
		return errOAuthStore
	}
	return nil
}

func authKeyID(key AuthKey) string {
	// Length-prefixed original bytes cannot alias through JSON's replacement of
	// invalid UTF-8. Public entry points reject such strings before hashing.
	var data []byte
	for _, value := range []string{key.Server, key.URL, key.Identity, key.Issuer, key.ClientID} {
		data = binary.BigEndian.AppendUint64(data, uint64(len(value)))
		data = append(data, value...)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func authValidateKeyFields(key AuthKey, deriveClient bool) error {
	for _, field := range []struct {
		value    string
		max      int
		optional bool
	}{
		{key.Server, 128, false}, {key.URL, 4096, false}, {key.Identity, 256, false},
		{key.Issuer, 4096, false}, {key.ClientID, 4096, deriveClient},
	} {
		if field.optional && field.value == "" {
			// Only the client binding can be derived from SDK configuration.
			continue
		}
		if !authValidKeyString(field.value, field.max) {
			return ErrOAuthConfiguration
		}
	}
	return nil
}

func authValidKeyString(value string, maxBytes int) bool {
	return len(value) <= maxBytes && strings.TrimSpace(value) != "" && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func cloneCredentialSnapshot(value CredentialSnapshot) CredentialSnapshot {
	return CredentialSnapshot{Version: value.Version, Credential: cloneOAuthCredential(value.Credential)}
}

func cloneOAuthCredential(value *OAuthCredential) *OAuthCredential {
	if value == nil {
		return nil
	}
	result := *value
	result.Scopes = append([]string(nil), value.Scopes...)
	if value.Token != nil {
		token := *value.Token
		result.Token = &token
	}
	return &result
}
