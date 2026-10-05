package mcpresources

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	managed "github.com/Icatme/pi-go/mcp"
)

// ResourceArtifact is a host-only request. Data never enters the model context
// for arbitrary binary resources. Scope binds it to the actual MCP identity.
type ResourceArtifact struct {
	Scope    managed.Scope
	Server   string
	URI      string
	MIMEType string
	SHA256   string
	Data     []byte
}

// ArtifactDescriptor is safe model metadata. Scope and Path are host-only;
// ScopeKey is a digest of identity and authorization epoch, never credentials.
type ArtifactDescriptor struct {
	ID       string        `json:"id"`
	ScopeKey string        `json:"scope"`
	Server   string        `json:"server"`
	URI      string        `json:"uri"`
	MIMEType string        `json:"mimeType"`
	SHA256   string        `json:"sha256"`
	Bytes    int           `json:"bytes"`
	Scope    managed.Scope `json:"-"`
	Path     string        `json:"-"`
}

// ArtifactSink is an explicit host capability. Implementations must honor
// cancellation, retain scope/server/URI/MIME/hash binding, bound writes and
// return a generated ID. They must not execute HTML or fetch referenced assets.
type ArtifactSink interface {
	WriteArtifact(context.Context, ResourceArtifact) (ArtifactDescriptor, error)
}

// FileArtifactStore writes inert .blob files only inside an explicitly supplied
// existing root. It creates no home/temp directory and never derives a filename
// from a resource URI, MIME type, server name or user identity. Root permissions
// and retention/cleanup are host responsibilities; Windows needs a private ACL.
type FileArtifactStore struct {
	rootPath string
	root     *os.Root
	maxBytes int
}

func NewFileArtifactStore(root string, maxBytes int) (*FileArtifactStore, error) {
	if !filepath.IsAbs(root) || maxBytes < 0 || maxBytes > 16<<20 {
		return nil, errors.New("mcpresources: invalid artifact root or byte bound")
	}
	if maxBytes == 0 {
		maxBytes = 1 << 20
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return nil, fmt.Errorf("mcpresources: artifact root is unavailable: %w", err)
	}
	handle, err := os.OpenRoot(resolved)
	if err != nil {
		return nil, fmt.Errorf("mcpresources: artifact root is unavailable: %w", err)
	}
	return &FileArtifactStore{rootPath: resolved, root: handle, maxBytes: maxBytes}, nil
}

// Close closes the directory handle after the host has stopped artifact writes.
// Stored artifacts remain until the host's retention policy removes them.
func (s *FileArtifactStore) Close() error { return s.root.Close() }

func (s *FileArtifactStore) WriteArtifact(ctx context.Context, artifact ResourceArtifact) (descriptor ArtifactDescriptor, err error) {
	if err := ctx.Err(); err != nil {
		return ArtifactDescriptor{}, err
	}
	// Public callers supply both the bytes and their digest. Keep validating
	// that binding before accepting data from outside the resource converter.
	if len(artifact.Data) > s.maxBytes || artifact.SHA256 != artifactDigest(artifact.Data) {
		return ArtifactDescriptor{}, errors.New("mcpresources: invalid artifact binding or size")
	}
	return s.writeOwnedArtifact(ctx, artifact)
}

// writeOwnedArtifact accepts the private copy and digest constructed by
// convertRead, or a request already checked by WriteArtifact. Rehashing those
// bytes before writing would not verify their persistence on disk.
func (s *FileArtifactStore) writeOwnedArtifact(ctx context.Context, artifact ResourceArtifact) (descriptor ArtifactDescriptor, err error) {
	if err := ctx.Err(); err != nil {
		return ArtifactDescriptor{}, err
	}
	if len(artifact.Data) > s.maxBytes || artifact.Scope.Identity == "" || artifact.Scope.AuthEpoch == 0 || artifact.Server == "" || len(artifact.Server) > 128 || validURI(artifact.URI, 16<<10) != nil {
		return ArtifactDescriptor{}, errors.New("mcpresources: invalid artifact binding or size")
	}
	if _, err := validMIME(artifact.MIMEType, 256, ""); err != nil || artifact.MIMEType == "" {
		return ArtifactDescriptor{}, errors.New("mcpresources: invalid artifact MIME")
	}
	descriptor = ArtifactDescriptor{
		ID: rand.Text(), ScopeKey: artifactScopeKey(artifact.Scope), Scope: artifact.Scope,
		Server: artifact.Server, URI: artifact.URI, MIMEType: artifact.MIMEType,
		SHA256: artifact.SHA256, Bytes: len(artifact.Data),
	}
	name := "mcp-resource-" + descriptor.ScopeKey[:16] + "-" + descriptor.ID + ".blob"
	f, err := s.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ArtifactDescriptor{}, fmt.Errorf("mcpresources: artifact file could not be created: %w", err)
	}
	complete := false
	closed := false
	defer func() {
		if !closed {
			if closeErr := f.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("mcpresources: artifact cleanup close failed: %w", closeErr))
			}
		}
		if !complete {
			if removeErr := s.root.Remove(name); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, fmt.Errorf("mcpresources: artifact cleanup removal failed: %w", removeErr))
			}
		}
	}()
	for offset := 0; offset < len(artifact.Data); {
		if err := ctx.Err(); err != nil {
			return ArtifactDescriptor{}, err
		}
		end := min(offset+(32<<10), len(artifact.Data))
		written, err := f.Write(artifact.Data[offset:end])
		if err != nil {
			return ArtifactDescriptor{}, fmt.Errorf("mcpresources: artifact write failed: %w", err)
		}
		if written != end-offset {
			return ArtifactDescriptor{}, fmt.Errorf("mcpresources: artifact write failed: %w", io.ErrShortWrite)
		}
		offset = end
	}
	if err := f.Sync(); err != nil {
		return ArtifactDescriptor{}, fmt.Errorf("mcpresources: artifact sync failed: %w", err)
	}
	closed = true
	if err := f.Close(); err != nil {
		return ArtifactDescriptor{}, fmt.Errorf("mcpresources: artifact close failed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return ArtifactDescriptor{}, err
	}
	descriptor.Path = filepath.Join(s.rootPath, name)
	complete = true
	return descriptor, nil
}

func validateDescriptor(artifact ResourceArtifact, descriptor ArtifactDescriptor) error {
	if descriptor.Scope != artifact.Scope || descriptor.ScopeKey != artifactScopeKey(artifact.Scope) || descriptor.Server != artifact.Server || descriptor.URI != artifact.URI || descriptor.MIMEType != artifact.MIMEType || descriptor.SHA256 != artifact.SHA256 || descriptor.Bytes != len(artifact.Data) || descriptor.ID == "" || len(descriptor.ID) > 128 {
		return errors.New("mcpresources: host artifact descriptor binding mismatch")
	}
	for _, value := range descriptor.ID {
		if !(value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || strings.ContainsRune("_-", value)) {
			return errors.New("mcpresources: host artifact descriptor ID is invalid")
		}
	}
	return nil
}

func artifactScopeKey(scope managed.Scope) string {
	data, _ := json.Marshal(scope)
	return artifactDigest(data)
}

func artifactDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
