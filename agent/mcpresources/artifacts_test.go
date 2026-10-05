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

func artifactTestDescriptor(artifact ResourceArtifact) ArtifactDescriptor {
	return ArtifactDescriptor{
		ID: "test-artifact", ScopeKey: artifactScopeKey(artifact.Scope), Scope: artifact.Scope,
		Server: artifact.Server, URI: artifact.URI, MIMEType: artifact.MIMEType,
		SHA256: artifact.SHA256, Bytes: len(artifact.Data),
	}
}
