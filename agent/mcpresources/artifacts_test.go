package mcpresources

import (
	"bytes"
	"context"
	"os"
	"testing"

	managed "github.com/Icatme/pi-go/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFileArtifactStoreChecksPublicDigest(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileArtifactStore(root, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	artifact := ResourceArtifact{Scope: managed.Scope{Identity: "alice", AuthEpoch: 1}, Server: "fixture", URI: "demo://blob", MIMEType: "application/octet-stream", Data: []byte("blob")}
	for _, digest := range []string{"", artifactDigest([]byte("other"))} {
		artifact.SHA256 = digest
		if _, err := store.WriteArtifact(t.Context(), artifact); err == nil {
			t.Fatalf("public write accepted mismatched digest %q", digest)
		}
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 0 {
		t.Fatalf("invalid public writes left files: files=%v err=%v", files, err)
	}
	artifact.SHA256 = artifactDigest(artifact.Data)
	descriptor, err := store.WriteArtifact(t.Context(), artifact)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(descriptor.Path)
	if err != nil || !bytes.Equal(stored, artifact.Data) || descriptor.SHA256 != artifact.SHA256 {
		t.Fatalf("valid public write lost bytes or digest: descriptor=%+v data=%q err=%v", descriptor, stored, err)
	}
}

func TestConvertReadArtifactOwnsPayloadAndKeepsDigest(t *testing.T) {
	data := []byte("blob")
	var received ResourceArtifact
	sink := artifactSinkFunc(func(_ context.Context, artifact ResourceArtifact) (ArtifactDescriptor, error) {
		received = artifact
		return artifactTestDescriptor(artifact), nil
	})
	o, err := (Options{ArtifactSink: sink}).defaults()
	if err != nil {
		t.Fatal(err)
	}
	result, err := convertRead(t.Context(), managed.Scope{Identity: "alice", AuthEpoch: 1}, "fixture", "demo://blob", &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: "demo://blob", Blob: data}}}, o, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := artifactDigest(data)
	descriptors := result.Details.([]ArtifactDescriptor)
	if !bytes.Equal(received.Data, data) || received.SHA256 != wantDigest || len(descriptors) != 1 || descriptors[0].SHA256 != wantDigest {
		t.Fatalf("sink or result lost payload binding: request=%+v descriptors=%+v", received, descriptors)
	}
	data[0] = 'B'
	if received.Data[0] != 'b' {
		t.Fatal("source mutation changed the sink's owned copy")
	}
	received.Data[1] = 'L'
	if data[1] != 'l' {
		t.Fatal("sink mutation changed the source payload")
	}
}

func TestConvertReadRejectsCustomDescriptorMismatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ArtifactDescriptor)
	}{
		{"scope", func(d *ArtifactDescriptor) { d.Scope.Identity = "bob" }},
		{"scope key", func(d *ArtifactDescriptor) { d.ScopeKey = "other" }},
		{"server", func(d *ArtifactDescriptor) { d.Server = "other" }},
		{"URI", func(d *ArtifactDescriptor) { d.URI = "demo://other" }},
		{"MIME", func(d *ArtifactDescriptor) { d.MIMEType = "text/plain" }},
		{"digest", func(d *ArtifactDescriptor) { d.SHA256 = artifactDigest([]byte("other")) }},
		{"bytes", func(d *ArtifactDescriptor) { d.Bytes++ }},
		{"empty ID", func(d *ArtifactDescriptor) { d.ID = "" }},
		{"invalid ID", func(d *ArtifactDescriptor) { d.ID = "../outside" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			sink := artifactSinkFunc(func(_ context.Context, artifact ResourceArtifact) (ArtifactDescriptor, error) {
				descriptor := artifactTestDescriptor(artifact)
				test.mutate(&descriptor)
				return descriptor, nil
			})
			o, err := (Options{ArtifactSink: sink}).defaults()
			if err != nil {
				t.Fatal(err)
			}
			result, err := convertRead(t.Context(), managed.Scope{Identity: "alice", AuthEpoch: 1}, "fixture", "demo://blob", &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: "demo://blob", Blob: []byte("blob")}}}, o, nil)
			if err == nil || len(result.StructuredContent) != 0 || len(result.Content) != 0 {
				t.Fatalf("custom descriptor mismatch was published: result=%+v err=%v", result, err)
			}
		})
	}
}

type overridingArtifactStore struct {
	*FileArtifactStore
	calls int
}

func (s *overridingArtifactStore) WriteArtifact(ctx context.Context, artifact ResourceArtifact) (ArtifactDescriptor, error) {
	s.calls++
	return s.FileArtifactStore.WriteArtifact(ctx, artifact)
}

func TestConvertReadCallsCustomFileStoreOverride(t *testing.T) {
	store, err := NewFileArtifactStore(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	sink := &overridingArtifactStore{FileArtifactStore: store}
	o, err := (Options{ArtifactSink: sink}).defaults()
	if err != nil {
		t.Fatal(err)
	}
	_, err = convertRead(t.Context(), managed.Scope{Identity: "alice", AuthEpoch: 1}, "fixture", "demo://blob", &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: "demo://blob", Blob: []byte("blob")}}}, o, nil)
	if err != nil || sink.calls != 1 {
		t.Fatalf("custom file store override bypassed: calls=%d err=%v", sink.calls, err)
	}
}

func TestConvertReadHonorsFileStoreByteLimit(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileArtifactStore(root, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	o, err := (Options{ArtifactSink: store}).defaults()
	if err != nil {
		t.Fatal(err)
	}
	_, err = convertRead(t.Context(), managed.Scope{Identity: "alice", AuthEpoch: 1}, "fixture", "demo://blob", &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: "demo://blob", Blob: []byte("oversized")}}}, o, nil)
	if err == nil {
		t.Fatal("owned artifact bypassed the file store's byte bound")
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 0 {
		t.Fatalf("oversized owned artifact left files: files=%v err=%v", files, err)
	}
}

func artifactTestDescriptor(artifact ResourceArtifact) ArtifactDescriptor {
	return ArtifactDescriptor{
		ID: "test-artifact", ScopeKey: artifactScopeKey(artifact.Scope), Scope: artifact.Scope,
		Server: artifact.Server, URI: artifact.URI, MIMEType: artifact.MIMEType,
		SHA256: artifact.SHA256, Bytes: len(artifact.Data),
	}
}

// BenchmarkConvertReadArtifact measures full conversion with either an in-memory
// descriptor sink or the built-in file store. File runs include sync and cleanup.
func BenchmarkConvertReadArtifact(b *testing.B) {
	for _, size := range []struct {
		name  string
		bytes int
	}{{"64KiB", 64 << 10}, {"1MiB", 1 << 20}} {
		for _, destination := range []string{"memory", "file"} {
			b.Run(destination+"/"+size.name, func(b *testing.B) {
				var sink ArtifactSink = artifactSinkFunc(func(_ context.Context, artifact ResourceArtifact) (ArtifactDescriptor, error) {
					return artifactTestDescriptor(artifact), nil
				})
				if destination == "file" {
					store, err := NewFileArtifactStore(b.TempDir(), size.bytes)
					if err != nil {
						b.Fatal(err)
					}
					b.Cleanup(func() { store.Close() })
					sink = store
				}
				o, err := (Options{ArtifactSink: sink}).defaults()
				if err != nil {
					b.Fatal(err)
				}
				response := &sdk.ReadResourceResult{Contents: []*sdk.ResourceContents{{URI: "demo://blob", Blob: bytes.Repeat([]byte("x"), size.bytes)}}}
				scope := managed.Scope{Identity: "alice", AuthEpoch: 1}
				b.SetBytes(int64(size.bytes))
				b.ReportAllocs()
				for b.Loop() {
					result, err := convertRead(b.Context(), scope, "fixture", "demo://blob", response, o, nil)
					if err != nil {
						b.Fatal(err)
					}
					if destination == "file" {
						if err := os.Remove(result.Details.([]ArtifactDescriptor)[0].Path); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}
