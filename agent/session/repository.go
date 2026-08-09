package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Repository owns durable sessions and grants at most one writer for a
// session at a time. A writer claim is released when the returned Session is
// closed.
type Repository interface {
	Create(header Header, options Options) (*Session, error)
	Open(id string, options Options) (*Session, error)
	List() ([]Header, error)
}

type memoryRecord struct {
	header Header
	log    []LogItem
}

// MemoryRepository keeps closed session logs in process memory. Reopening a
// session builds a fresh writable storage handle from its durable log.
type MemoryRepository struct {
	mu      sync.Mutex
	records map[string]memoryRecord
	claimed map[string]struct{}
}

// NewMemoryRepository returns an empty in-process repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		records: make(map[string]memoryRecord),
		claimed: make(map[string]struct{}),
	}
}

// Create creates and claims a new session.
func (r *MemoryRepository) Create(header Header, options Options) (*Session, error) {
	normalized, err := normalizeNewHeader(header)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureInitialized()
	if _, exists := r.records[normalized.ID]; exists {
		return nil, repositoryClaimError(normalized.ID)
	}

	storage, err := NewMemoryStorage(normalized)
	if err != nil {
		return nil, err
	}
	r.records[normalized.ID] = memoryRecord{header: storage.Header()}
	r.claimed[normalized.ID] = struct{}{}
	return r.newSessionLocked(normalized.ID, storage, options)
}

// Open claims an existing session for append.
func (r *MemoryRepository) Open(id string, options Options) (*Session, error) {
	if err := validateRepositoryID(id); err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureInitialized()
	record, exists := r.records[id]
	if !exists {
		return nil, sessionError(ErrorNotFound, fmt.Sprintf("session %q does not exist", id), nil)
	}
	if _, claimed := r.claimed[id]; claimed {
		return nil, repositoryClaimError(id)
	}

	storage, err := memoryStorageFromRecord(record)
	if err != nil {
		return nil, err
	}
	r.claimed[id] = struct{}{}
	return r.newSessionLocked(id, storage, options)
}

// List returns detached headers ordered by creation time and then id.
func (r *MemoryRepository) List() ([]Header, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensureInitialized()
	headers := make([]Header, 0, len(r.records))
	for _, record := range r.records {
		headers = append(headers, record.header)
	}
	sortHeaders(headers)
	return headers, nil
}

func (r *MemoryRepository) newSessionLocked(id string, storage *MemoryStorage, options Options) (*Session, error) {
	wrapped := &repositoryStorage{
		Storage: storage,
		release: func() {
			record := memoryRecord{
				header: storage.Header(),
				log:    storage.Log(),
			}
			r.mu.Lock()
			r.records[id] = record
			delete(r.claimed, id)
			r.mu.Unlock()
		},
	}
	session, err := New(wrapped, options)
	if err != nil {
		_ = storage.Close()
		delete(r.claimed, id)
		return nil, err
	}
	return session, nil
}

func (r *MemoryRepository) ensureInitialized() {
	if r.records == nil {
		r.records = make(map[string]memoryRecord)
	}
	if r.claimed == nil {
		r.claimed = make(map[string]struct{})
	}
}

func memoryStorageFromRecord(record memoryRecord) (*MemoryStorage, error) {
	state := newLogState(record.header)
	for _, item := range record.log {
		if err := state.apply(item); err != nil {
			return nil, sessionError(ErrorCorruptLog, fmt.Sprintf("replay in-memory session %q", record.header.ID), err)
		}
	}
	return &MemoryStorage{state: state}, nil
}

// JSONLRepository stores each session at root/<id>.jsonl. Writer claims are
// scoped to one repository instance; they are not cross-process file locks.
type JSONLRepository struct {
	mu      sync.Mutex
	root    string
	claimed map[string]struct{}
}

// NewJSONLRepository opens or creates a JSONL repository directory.
func NewJSONLRepository(root string) (*JSONLRepository, error) {
	if strings.TrimSpace(root) == "" {
		return nil, sessionError(ErrorStorage, "session repository root is empty", nil)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, sessionError(ErrorStorage, fmt.Sprintf("resolve session repository root %s", root), err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, sessionError(ErrorStorage, fmt.Sprintf("create session repository root %s", absolute), err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, sessionError(ErrorStorage, fmt.Sprintf("inspect session repository root %s", absolute), err)
	}
	if !info.IsDir() {
		return nil, sessionError(ErrorStorage, fmt.Sprintf("session repository root is not a directory: %s", absolute), nil)
	}
	return &JSONLRepository{root: absolute, claimed: make(map[string]struct{})}, nil
}

// Create creates and claims a new session file without overwriting an existing
// one.
func (r *JSONLRepository) Create(header Header, options Options) (*Session, error) {
	normalized, err := normalizeNewHeader(header)
	if err != nil {
		return nil, err
	}
	path, err := r.pathForID(normalized.ID)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, claimed := r.claimed[normalized.ID]; claimed {
		return nil, repositoryClaimError(normalized.ID)
	}
	storage, err := CreateJSONLStorage(path, normalized)
	if err != nil {
		return nil, err
	}
	r.claimed[normalized.ID] = struct{}{}
	return r.newSessionLocked(normalized.ID, storage, options)
}

// Open validates and claims an existing session file for append.
func (r *JSONLRepository) Open(id string, options Options) (*Session, error) {
	path, err := r.pathForID(id)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, claimed := r.claimed[id]; claimed {
		return nil, repositoryClaimError(id)
	}
	pathInfo, err := inspectRepositoryFile(path)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, sessionError(ErrorNotFound, fmt.Sprintf("session file does not exist: %s", path), err)
		}
		return nil, sessionError(ErrorStorage, fmt.Sprintf("open session file %s", path), err)
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, sessionError(ErrorStorage, fmt.Sprintf("inspect opened session file %s", path), err)
	}
	if !os.SameFile(pathInfo, openedInfo) {
		_ = file.Close()
		return nil, sessionError(ErrorStorage, fmt.Sprintf("session file changed while opening: %s", path), nil)
	}
	storage, err := openJSONLStorageFile(path, file)
	if err != nil {
		return nil, err
	}
	if storage.Header().ID != id {
		_ = storage.Close()
		return nil, sessionError(ErrorCorruptLog, fmt.Sprintf("session file id %q does not match requested id %q", storage.Header().ID, id), nil)
	}
	r.claimed[id] = struct{}{}
	return r.newSessionLocked(id, storage, options)
}

// List reads only immutable JSONL headers. It does not acquire writer claims.
func (r *JSONLRepository) List() ([]Header, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entries, err := os.ReadDir(r.root)
	if err != nil {
		return nil, sessionError(ErrorStorage, fmt.Sprintf("list session repository %s", r.root), err)
	}

	headers := make([]Header, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, sessionError(ErrorStorage, fmt.Sprintf("inspect session file %s", entry.Name()), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		path := filepath.Join(r.root, entry.Name())
		file, err := os.Open(path)
		if err != nil {
			return nil, sessionError(ErrorStorage, fmt.Sprintf("open session header %s", path), err)
		}
		openedInfo, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			return nil, sessionError(ErrorStorage, fmt.Sprintf("inspect opened session file %s", path), statErr)
		}
		if !os.SameFile(info, openedInfo) {
			_ = file.Close()
			return nil, sessionError(ErrorStorage, fmt.Sprintf("session file changed while listing: %s", path), nil)
		}
		header, readErr := readJSONLHeaderFile(path, file)
		closeErr := file.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, sessionError(ErrorStorage, fmt.Sprintf("close session header %s", path), closeErr)
		}
		fileID := strings.TrimSuffix(entry.Name(), ".jsonl")
		if header.ID != fileID {
			return nil, sessionError(ErrorCorruptLog, fmt.Sprintf("session file %s contains id %q", path, header.ID), nil)
		}
		headers = append(headers, header)
	}
	sortHeaders(headers)
	return headers, nil
}

func (r *JSONLRepository) newSessionLocked(id string, storage *JSONLStorage, options Options) (*Session, error) {
	wrapped := &repositoryStorage{
		Storage: storage,
		release: func() {
			r.mu.Lock()
			delete(r.claimed, id)
			r.mu.Unlock()
		},
	}
	session, err := New(wrapped, options)
	if err != nil {
		_ = storage.Close()
		delete(r.claimed, id)
		return nil, err
	}
	return session, nil
}

func (r *JSONLRepository) pathForID(id string) (string, error) {
	if err := validateRepositoryID(id); err != nil {
		return "", err
	}
	path := filepath.Join(r.root, id+".jsonl")
	relative, err := filepath.Rel(r.root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", sessionError(ErrorInvalidHeader, "session id escapes repository root", err)
	}
	return path, nil
}

func validateRepositoryID(id string) error {
	if !sessionIDPattern.MatchString(id) || filepath.Base(id) != id || filepath.IsAbs(id) || filepath.VolumeName(id) != "" {
		return sessionError(ErrorInvalidHeader, "session id is not safe for repository storage", nil)
	}
	return nil
}

func inspectRepositoryFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, sessionError(ErrorNotFound, fmt.Sprintf("session file does not exist: %s", path), err)
		}
		return nil, sessionError(ErrorStorage, fmt.Sprintf("inspect session file %s", path), err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, sessionError(ErrorStorage, fmt.Sprintf("session path is not a regular file: %s", path), nil)
	}
	return info, nil
}

func repositoryClaimError(id string) error {
	return sessionError(ErrorAlreadyExists, fmt.Sprintf("session %q already has an active writer", id), nil)
}

func sortHeaders(headers []Header) {
	sort.Slice(headers, func(i, j int) bool {
		if headers[i].CreatedAt != headers[j].CreatedAt {
			return headers[i].CreatedAt < headers[j].CreatedAt
		}
		return headers[i].ID < headers[j].ID
	})
}

type repositoryStorage struct {
	Storage
	closeOnce sync.Once
	closeErr  error
	release   func()
}

func (s *repositoryStorage) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.Storage.Close()
		if s.release != nil {
			s.release()
		}
	})
	return s.closeErr
}

var (
	_ Repository = (*MemoryRepository)(nil)
	_ Repository = (*JSONLRepository)(nil)
	_ Storage    = (*repositoryStorage)(nil)
)
