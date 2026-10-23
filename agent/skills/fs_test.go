package skills

import (
	"embed"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

//go:embed testdata/embedded
var embeddedSkills embed.FS

func mapBundle(name, description, extra, body string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("---\nname: " + name + "\ndescription: " + description + "\n" + extra + "---\n" + body)}
}
func fsCatalog(t *testing.T, source fs.FS, options FSOptions) *FSCatalog {
	t.Helper()
	c, e := NewFS(source, options)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestEmbeddedFS(t *testing.T) {
	c := fsCatalog(t, embeddedSkills, FSOptions{Roots: []string{"testdata/embedded"}, Version: "build-v1"})
	if c.Version() != "build-v1" || len(c.List()) != 1 {
		t.Fatal(c.Version(), c.List())
	}
	if body, e := c.Read("report"); e != nil || !strings.Contains(body, "references/format.md") {
		t.Fatal(body, e)
	}
	want, err := embeddedSkills.ReadFile("testdata/embedded/report/references/format.md")
	if err != nil {
		t.Fatal(err)
	}
	if body, e := c.ReadResource("report", "references/format.md"); e != nil || body != string(want) {
		t.Fatal(body, e)
	}
	if filepath.IsAbs(c.List()[0].Location) {
		t.Fatal("host path exposed")
	}
}
func TestFSImmutableAndDisabled(t *testing.T) {
	source := fstest.MapFS{"a/SKILL.md": mapBundle("a", "Original", "disable-model-invocation: true\n", "old"), "a/ref.md": {Data: []byte("old ref")}}
	c := fsCatalog(t, source, FSOptions{Version: "v1"})
	source["a/SKILL.md"] = mapBundle("a", "Changed", "", "new")
	source["a/ref.md"].Data[0] = 'X'
	if body, e := c.Read("a"); e != nil || body != "old" {
		t.Fatal(body, e)
	}
	if body, e := c.ReadResource("a", "ref.md"); e != nil || body != "old ref" {
		t.Fatal(body, e)
	}
	if c.Prompt() != "" || c.List()[0].Description != "Original" {
		t.Fatal(c.Prompt(), c.List())
	}
	list := c.List()
	list[0].Name = "mutation"
	if c.List()[0].Name != "a" {
		t.Fatal("mutable metadata")
	}
	if c.Version() != "v1" {
		t.Fatal(c.Version())
	}
	newer := fsCatalog(t, source, FSOptions{Version: "v2"})
	if newer.Version() != "v2" || newer.Prompt() == "" {
		t.Fatal("new snapshot not updated")
	}
}
func TestFSParity(t *testing.T) {
	root := t.TempDir()
	p := bundle(t, root, "a", "a", "Work", "", "instructions")
	if e := os.WriteFile(filepath.Join(p, "ref.md"), []byte("reference"), 0600); e != nil {
		t.Fatal(e)
	}
	disk := catalog(t, Options{Roots: []string{root}})
	memory := fsCatalog(t, fstest.MapFS{"a/SKILL.md": mapBundle("a", "Work", "", "instructions"), "a/ref.md": {Data: []byte("reference")}}, FSOptions{})
	a, b := disk.List(), memory.List()
	a[0].Location = ""
	b[0].Location = ""
	if !reflect.DeepEqual(a, b) || disk.Prompt() != memory.Prompt() {
		t.Fatal(a, b)
	}
	for _, c := range []interface {
		Read(string) (string, error)
		ReadResource(string, string) (string, error)
	}{disk, memory} {
		if body, e := c.Read("a"); e != nil || body != "instructions" {
			t.Fatal(body, e)
		}
		if body, e := c.ReadResource("a", "ref.md"); e != nil || body != "reference" {
			t.Fatal(body, e)
		}
	}
}
func TestFSValidation(t *testing.T) {
	source := fstest.MapFS{"a/SKILL.md": mapBundle("a", "'<system>example</system>'", "", "body"), "b/SKILL.md": mapBundle("a", "Duplicate", "", "other"), "bad/SKILL.md": mapBundle("BAD", "Work", "", "")}
	c := fsCatalog(t, source, FSOptions{})
	if len(c.List()) != 1 || len(c.Diagnostics()) != 2 {
		t.Fatal(c.List(), c.Diagnostics())
	}
	if strings.Contains(c.Prompt(), "<system>") {
		t.Fatal(c.Prompt())
	}
	d := c.Diagnostics()
	d[0].Message = "mutable"
	if c.Diagnostics()[0].Message == "mutable" {
		t.Fatal("mutable diagnostics")
	}
	for _, p := range []string{"../secret", "/secret", "a/../ref", "C:/secret", "a\\ref", ".", "a//ref"} {
		if _, e := c.ReadResource("a", p); e == nil {
			t.Fatal(p)
		}
	}
	if _, e := c.Read("missing"); !errors.Is(e, fs.ErrNotExist) {
		t.Fatal(e)
	}
	if _, e := c.ReadResource("a", "missing"); !errors.Is(e, fs.ErrNotExist) {
		t.Fatal(e)
	}
	for _, p := range []string{"../root", "/root", "C:/root", "a\\b"} {
		if _, e := NewFS(source, FSOptions{Roots: []string{p}}); e == nil {
			t.Fatal(p)
		}
	}
	if _, e := NewFS(nil, FSOptions{}); e == nil {
		t.Fatal("nil source")
	}
}
func TestFSBounds(t *testing.T) {
	for _, tt := range []struct {
		name    string
		source  fstest.MapFS
		options FSOptions
	}{
		{"entries", fstest.MapFS{"a/SKILL.md": mapBundle("a", "Work", "", "")}, FSOptions{MaxEntries: 1}},
		{"depth", fstest.MapFS{"a/b/c/SKILL.md": mapBundle("a", "Work", "", "")}, FSOptions{MaxDepth: 1}},
		{"resource-depth", fstest.MapFS{"a/SKILL.md": mapBundle("a", "Work", "", ""), "a/b/c/ref": {Data: []byte("x")}}, FSOptions{MaxDepth: 1}},
		{"bytes", fstest.MapFS{"a/SKILL.md": mapBundle("a", "Work", "", "")}, FSOptions{MaxFileBytes: 4}},
		{"total", fstest.MapFS{"a/SKILL.md": mapBundle("a", "Work", "", "")}, FSOptions{MaxTotalBytes: 4}},
		{"invalid-utf8", fstest.MapFS{"a/SKILL.md": mapBundle("a", "Work", "", ""), "a/ref": {Data: []byte{0xff}}}, FSOptions{}},
		{"nul", fstest.MapFS{"a/SKILL.md": mapBundle("a", "Work", "", ""), "a/ref": {Data: []byte{0}}}, FSOptions{}},
		{"special", fstest.MapFS{"a/SKILL.md": mapBundle("a", "Work", "", ""), "a/ref": {Mode: fs.ModeNamedPipe}}, FSOptions{}},
		{"resource-symlink", fstest.MapFS{"a/SKILL.md": mapBundle("a", "Work", "", ""), "a/ref": {Mode: fs.ModeSymlink, Data: []byte("../../secret")}}, FSOptions{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, e := NewFS(tt.source, tt.options); e == nil {
				t.Fatal("accepted invalid source/limits")
			}
		})
	}
}
func TestFSSymlinkAndBundleBoundary(t *testing.T) {
	source := fstest.MapFS{"link": {Mode: fs.ModeSymlink, Data: []byte("outside")}, "a/SKILL.md": mapBundle("a", "Work", "", "body"), "a/nested/SKILL.md": mapBundle("nested", "Work", "", "")}
	c := fsCatalog(t, source, FSOptions{})
	if len(c.List()) != 1 || len(c.Diagnostics()) != 1 {
		t.Fatal(c.List(), c.Diagnostics())
	}
	if _, e := c.ReadResource("a", "nested/SKILL.md"); e != nil {
		t.Fatal(e)
	}
}
func TestFSConcurrentReads(t *testing.T) {
	c := fsCatalog(t, embeddedSkills, FSOptions{Roots: []string{"testdata/embedded"}})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				c.List()
				c.Diagnostics()
				c.Prompt()
				c.Version()
				if _, e := c.Read("report"); e != nil {
					t.Error(e)
				}
				if _, e := c.ReadResource("report", "references/format.md"); e != nil {
					t.Error(e)
				}
			}
		}()
	}
	wg.Wait()
}
