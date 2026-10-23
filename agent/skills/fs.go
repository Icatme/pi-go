package skills

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

// FSOptions configures a trusted in-process filesystem, normally embed.FS.
// Roots are slash-separated fs.ValidPath directories, defaulting to ".".
// Version is an application-assigned build/content version, not a verified hash.
type FSOptions struct {
	Roots         []string
	MaxFileBytes  int64 // default 256 KiB, maximum 16 MiB
	MaxTotalBytes int64 // default 16 MiB, maximum 256 MiB
	MaxEntries    int   // default 4096, counts discovery and bundled resources
	MaxDepth      int   // default 16, maximum 128
	Version       string
}

type fsEntry struct {
	body      string
	resources map[string]string
}

// FSCatalog is an immutable copy of approved skill text and resources. It is
// safe for concurrent reads and never accesses the source after construction.
// There is no reload, execution, installation, or filesystem configuration API.
type FSCatalog struct {
	entries     map[string]fsEntry
	ordered     []Metadata
	diagnostics []Diagnostic
	version     string
}

// NewFS snapshots text from a trusted FS (such as go:embed). The FS implementation
// itself is part of the trust boundary: fs.ValidPath is not a sandbox around an
// arbitrary FS, os.DirFS, remote backend, or malicious FS implementation.
// Discovered symlinks and special files are rejected. Supply compile-time bundles
// rather than writable external files when code review must govern all changes.
func NewFS(source fs.FS, options FSOptions) (*FSCatalog, error) {
	if source == nil {
		return nil, errors.New("skills: nil filesystem")
	}
	if len(options.Roots) == 0 {
		options.Roots = []string{"."}
	}
	if options.MaxFileBytes == 0 {
		options.MaxFileBytes = 256 << 10
	}
	if options.MaxTotalBytes == 0 {
		options.MaxTotalBytes = 16 << 20
	}
	if options.MaxEntries == 0 {
		options.MaxEntries = 4096
	}
	if options.MaxDepth == 0 {
		options.MaxDepth = 16
	}
	if options.MaxFileBytes < 1 || options.MaxFileBytes > 16<<20 || options.MaxTotalBytes < 1 || options.MaxTotalBytes > 256<<20 || options.MaxEntries < 1 || options.MaxDepth < 1 || options.MaxDepth > 128 {
		return nil, errors.New("skills: invalid limits")
	}
	c := &FSCatalog{entries: map[string]fsEntry{}, version: options.Version}
	scan := fsScanner{source: source, options: options, catalog: c}
	for _, root := range options.Roots {
		if !validFSPath(root) {
			return nil, errors.New("skills: invalid FS root")
		}
		if err := scan.discover(root, 0); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (c *FSCatalog) List() []Metadata          { return append([]Metadata(nil), c.ordered...) }
func (c *FSCatalog) Diagnostics() []Diagnostic { return append([]Diagnostic(nil), c.diagnostics...) }
func (c *FSCatalog) Prompt() string            { return formatPrompt(c.ordered) }

// Version returns the caller's build label. It does not imply cryptographic
// integrity, identity uniqueness, or authorization, and is never recalculated.
func (c *FSCatalog) Version() string { return c.version }
func (c *FSCatalog) Read(name string) (string, error) {
	e, ok := c.entries[name]
	if !ok {
		return "", fs.ErrNotExist
	}
	return e.body, nil
}
func (c *FSCatalog) ReadResource(name, resource string) (string, error) {
	if !validFSPath(resource) || resource == "." {
		return "", errors.New("skills: invalid resource path")
	}
	e, ok := c.entries[name]
	if !ok {
		return "", fs.ErrNotExist
	}
	text, ok := e.resources[resource]
	if !ok {
		return "", fs.ErrNotExist
	}
	return text, nil
}
func validFSPath(p string) bool { return fs.ValidPath(p) && !strings.ContainsAny(p, "\\:\x00") }

type fsScanner struct {
	source  fs.FS
	options FSOptions
	catalog *FSCatalog
	count   int
	total   int64
}

func (s *fsScanner) directory(p string, depth int) ([]fs.DirEntry, error) {
	if depth > s.options.MaxDepth {
		return nil, errors.New("skills: scan depth limit exceeded")
	}
	f, err := s.source.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rd, ok := f.(fs.ReadDirFile)
	if !ok {
		return nil, fmt.Errorf("skills: %s is not a readable directory", p)
	}
	var entries []fs.DirEntry
	for {
		batch, e := rd.ReadDir(128)
		s.count += len(batch)
		if s.count > s.options.MaxEntries {
			return nil, errors.New("skills: scan entry limit exceeded")
		}
		for _, entry := range batch {
			if !validFSPath(entry.Name()) || strings.Contains(entry.Name(), "/") || entry.Name() == "." {
				return nil, errors.New("skills: invalid directory entry")
			}
		}
		entries = append(entries, batch...)
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if len(batch) == 0 {
			return nil, io.ErrNoProgress
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}
func (s *fsScanner) diagnostic(p string, e error) {
	s.catalog.diagnostics = append(s.catalog.diagnostics, Diagnostic{p, e.Error()})
}
func (s *fsScanner) discover(dir string, depth int) error {
	entries, err := s.directory(dir, depth)
	if err != nil {
		return err
	}
	for _, item := range entries {
		if item.Name() != "SKILL.md" {
			continue
		}
		location := path.Join(dir, "SKILL.md")
		if !item.Type().IsRegular() {
			s.diagnostic(location, errors.New("skills: SKILL.md must be a regular non-symlink file"))
			return nil
		}
		raw, err := s.text(location)
		if err != nil {
			return err
		}
		m, body, err := parse(raw)
		if err != nil {
			s.diagnostic(location, err)
			return nil
		}
		m.Location = location
		if _, ok := s.catalog.entries[m.Name]; ok {
			s.diagnostic(location, fmt.Errorf("skills: duplicate name %q; first wins", m.Name))
			return nil
		}
		resources := map[string]string{"SKILL.md": raw}
		if err := s.resources(dir, ".", depth, entries, resources); err != nil {
			return err
		}
		s.catalog.entries[m.Name] = fsEntry{body: body, resources: resources}
		s.catalog.ordered = append(s.catalog.ordered, m)
		return nil
	}
	for _, item := range entries {
		p := path.Join(dir, item.Name())
		if item.Type()&fs.ModeSymlink != 0 {
			s.diagnostic(p, errors.New("skills: discovery skips symlinks"))
			continue
		}
		if !item.IsDir() || strings.HasPrefix(item.Name(), ".") || item.Name() == "node_modules" {
			continue
		}
		if err := s.discover(p, depth+1); err != nil {
			return err
		}
	}
	return nil
}
func (s *fsScanner) resources(dir, relative string, depth int, entries []fs.DirEntry, dest map[string]string) error {
	for _, item := range entries {
		if relative == "." && item.Name() == "SKILL.md" {
			continue
		}
		p := path.Join(dir, item.Name())
		r := path.Join(relative, item.Name())
		if item.IsDir() {
			children, err := s.directory(p, depth+1)
			if err != nil {
				return err
			}
			if err := s.resources(p, r, depth+1, children, dest); err != nil {
				return err
			}
			continue
		}
		if !item.Type().IsRegular() {
			return fmt.Errorf("skills: resource %s must be a regular non-symlink file", p)
		}
		text, err := s.text(p)
		if err != nil {
			return err
		}
		dest[r] = text
	}
	return nil
}
func (s *fsScanner) text(p string) (string, error) {
	f, err := s.source.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("skills: %s is not a regular file", p)
	}
	if info.Size() > s.options.MaxFileBytes {
		return "", fmt.Errorf("skills: %s exceeds size limit", p)
	}
	raw, err := io.ReadAll(io.LimitReader(f, s.options.MaxFileBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(raw)) > s.options.MaxFileBytes {
		return "", fmt.Errorf("skills: %s exceeds size limit", p)
	}
	s.total += int64(len(raw))
	if s.total > s.options.MaxTotalBytes {
		return "", errors.New("skills: total text size limit exceeded")
	}
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		return "", fmt.Errorf("skills: %s must be UTF-8 text without NUL", p)
	}
	return string(raw), nil
}
