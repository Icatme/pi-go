package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func authStoreTestKey() AuthKey {
	return AuthKey{Server: "server", URL: "https://mcp.example/mcp", Identity: "account", Issuer: "https://issuer.example", ClientID: "client"}
}

func authStoreTestCredential() *OAuthCredential {
	return &OAuthCredential{Binding: authStoreTestKey(), ClientID: "client", Scopes: []string{"read"}, Token: &oauth2.Token{AccessToken: "test-access"}}
}

func TestOAuthStoresRejectInvalidAuthKeyWithoutAliasing(t *testing.T) {
	fileStore, err := NewFileCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, store := range []struct {
		name  string
		store CredentialStore
	}{{"memory", NewMemoryCredentialStore()}, {"file", fileStore}} {
		t.Run(store.name, func(t *testing.T) {
			// JSON repairs an invalid byte into this character. The invalid key
			// must neither read nor replace the legitimate account's credential.
			valid := authStoreTestKey()
			valid.Identity = "account\ufffd"
			credential := authStoreTestCredential()
			credential.Binding = valid
			if _, err := store.store.CompareAndSwap(t.Context(), valid, 0, credential); err != nil {
				t.Fatal(err)
			}
			invalid := valid
			invalid.Identity = "account\xff"
			if _, err := store.store.Load(t.Context(), invalid); !errors.Is(err, ErrOAuthConfiguration) {
				t.Fatalf("invalid UTF-8 read aliased valid identity: %v", err)
			}
			if _, err := store.store.CompareAndSwap(t.Context(), invalid, 1, nil); !errors.Is(err, ErrOAuthConfiguration) {
				t.Fatalf("invalid UTF-8 CAS aliased valid identity: %v", err)
			}
			loaded, err := store.store.Load(t.Context(), valid)
			if err != nil || loaded.Version != 1 || loaded.Credential == nil {
				t.Fatalf("invalid key damaged legitimate entry: version=%d err=%v", loaded.Version, err)
			}
			for _, field := range []struct {
				name string
				max  int
				set  func(*AuthKey, string)
			}{
				{"server", 128, func(key *AuthKey, value string) { key.Server = value }},
				{"url", 4096, func(key *AuthKey, value string) { key.URL = value }},
				{"identity", 256, func(key *AuthKey, value string) { key.Identity = value }},
				{"issuer", 4096, func(key *AuthKey, value string) { key.Issuer = value }},
				{"client", 4096, func(key *AuthKey, value string) { key.ClientID = value }},
			} {
				for _, value := range []string{"", "\xff", "value\x00", "value\r", "value\n", strings.Repeat("x", field.max+1)} {
					key := authStoreTestKey()
					field.set(&key, value)
					if _, err := store.store.Load(t.Context(), key); !errors.Is(err, ErrOAuthConfiguration) {
						t.Fatalf("invalid %s Load accepted: %v", field.name, err)
					}
					if _, err := store.store.CompareAndSwap(t.Context(), key, 0, nil); !errors.Is(err, ErrOAuthConfiguration) {
						t.Fatalf("invalid %s CAS accepted: %v", field.name, err)
					}
				}
			}
		})
	}
}

func TestOAuthFileStoreCompareAndSwapAndTombstone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	first, err := NewFileCredentialStore(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewFileCredentialStore(path)
	if err != nil {
		t.Fatal(err)
	}
	key := authStoreTestKey()
	credential := authStoreTestCredential()
	if version, err := first.CompareAndSwap(t.Context(), key, 0, credential); err != nil || version != 1 {
		t.Fatalf("initial CAS: version=%d err=%v", version, err)
	}
	credential.Token.AccessToken = "mutated-caller-token"
	loaded, err := second.Load(t.Context(), key)
	if err != nil || loaded.Version != 1 || loaded.Credential.Token.AccessToken != "test-access" {
		t.Fatalf("store did not isolate caller mutation: version=%d err=%v", loaded.Version, err)
	}
	if _, err := second.CompareAndSwap(t.Context(), key, 0, authStoreTestCredential()); !errors.Is(err, ErrOAuthCredentialConflict) {
		t.Fatalf("stale version accepted: %v", err)
	}
	if version, err := first.CompareAndSwap(t.Context(), key, 1, nil); err != nil || version != 2 {
		t.Fatalf("clear CAS: version=%d err=%v", version, err)
	}
	loaded, err = second.Load(t.Context(), key)
	if err != nil || loaded.Version != 2 || loaded.Credential != nil {
		t.Fatalf("tombstone lost version: %+v err=%v", loaded, err)
	}
	if _, err := first.CompareAndSwap(t.Context(), key, 0, authStoreTestCredential()); !errors.Is(err, ErrOAuthCredentialConflict) {
		t.Fatalf("ABA version accepted: %v", err)
	}
	if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("normal completion left lock: %v", err)
	}
}

func TestOAuthFileStoreLockCancellationAndNoStaleBreak(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store, err := NewFileCredentialStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".lock", 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := store.CompareAndSwap(ctx, authStoreTestKey(), 0, authStoreTestCredential()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("locked store ignored cancellation: %v", err)
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatalf("store broke lock without writer proof: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled CAS published credentials: %v", err)
	}
}

func TestOAuthFileStoreLockDirectoryAndReleaseFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	store, err := NewFileCredentialStore(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := store.lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path + ".lock")
	if err != nil || !info.IsDir() {
		t.Fatalf("lock retains a Windows file handle lifecycle: info=%v err=%v", info, err)
	}
	// A second independent object must respect the same directory ownership.
	other, err := NewFileCredentialStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := other.Load(ctx, authStoreTestKey()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Load bypassed another object's lock: %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	// Lost ownership is a host-visible storage failure with the original cause;
	// it cannot be swallowed or presented as an ordinary successful release.
	err = unlock()
	if !errors.Is(err, errOAuthStore) || !errors.Is(err, os.ErrNotExist) || err.Error() != errOAuthStore.Error() {
		t.Fatalf("release failure lost safe error/cause: %v", err)
	}
}

func TestOAuthFileStoreErrorRetainsCauseWithoutPrivatePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-account-path", "credentials.json")
	_, err := NewFileCredentialStore(path)
	if !errors.Is(err, errOAuthStore) || !errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), path) {
		t.Fatalf("storage error lost cause or exposed caller path: %v", err)
	}
	var cause *os.PathError
	if !errors.As(err, &cause) {
		t.Fatal("storage error no longer exposes original Go OS cause to host")
	}
}

func TestOAuthFileStoreCrossProcessCAS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	const writers = 4
	results := make(chan string, writers)
	var group sync.WaitGroup
	for range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestOAuthFileStoreCASProcess$", "-test.timeout=20s")
			cmd.Env = append(os.Environ(), "PI_GO_OAUTH_STORE_HELPER=1", "PI_GO_OAUTH_STORE_PATH="+path)
			data, err := cmd.CombinedOutput()
			if err != nil {
				results <- fmt.Sprintf("failed: %v: %s", err, data)
				return
			}
			results <- string(data)
		}()
	}
	group.Wait()
	close(results)
	winners, conflicts := 0, 0
	for result := range results {
		if strings.Contains(result, "oauth-cas-winner") {
			winners++
		} else if strings.Contains(result, "oauth-cas-conflict") {
			conflicts++
		} else {
			t.Errorf("unexpected child result: %s", result)
		}
	}
	if winners != 1 || conflicts != writers-1 {
		t.Fatalf("cross-process CAS winners=%d conflicts=%d", winners, conflicts)
	}
	store, err := NewFileCredentialStore(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load(t.Context(), authStoreTestKey())
	if err != nil || snapshot.Version != 1 || snapshot.Credential == nil {
		t.Fatalf("incomplete published record: %+v err=%v", snapshot, err)
	}
}

func TestOAuthFileStoreCASProcess(t *testing.T) {
	if os.Getenv("PI_GO_OAUTH_STORE_HELPER") != "1" {
		return
	}
	store, err := NewFileCredentialStore(os.Getenv("PI_GO_OAUTH_STORE_PATH"))
	if err != nil {
		authStoreTestFatal(t, err)
	}
	if _, err := store.CompareAndSwap(t.Context(), authStoreTestKey(), 0, authStoreTestCredential()); err == nil {
		fmt.Println("oauth-cas-winner")
	} else if errors.Is(err, ErrOAuthCredentialConflict) {
		fmt.Println("oauth-cas-conflict")
	} else {
		authStoreTestFatal(t, err)
	}
}

func authStoreTestFatal(t *testing.T, err error) {
	t.Helper()
	var failure *authStoreFailure
	var pathError *os.PathError
	var linkError *os.LinkError
	if errors.As(err, &failure) {
		// OS errors describe the failed operation. Paths and JSON/token contents
		// are deliberately absent from child-process diagnostics.
		switch {
		case errors.As(failure.cause, &pathError):
			t.Fatalf("%v: operation=%s go_op=%s cause=%T: %v", err, failure.operation, pathError.Op, pathError.Err, pathError.Err)
		case errors.As(failure.cause, &linkError):
			t.Fatalf("%v: operation=%s go_op=%s cause=%T: %v", err, failure.operation, linkError.Op, linkError.Err, linkError.Err)
		default:
			t.Fatalf("%v: operation=%s cause_type=%T", err, failure.operation, failure.cause)
		}
	}
	t.Fatal(err)
}
