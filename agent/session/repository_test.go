package session

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func TestMemoryRepositoryReopensClosedSessionWithLog(t *testing.T) {
	repository := NewMemoryRepository()
	session, err := repository.Create(
		Header{ID: "memory-session", CreatedAt: 10},
		Options{IDGenerator: sequenceIDGenerator("entry-1")},
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := session.AppendMessage(MainLane, agent.NewUserTextMessage("hello")); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if _, err := repository.Open("memory-session", Options{}); !hasSessionErrorCode(err, ErrorAlreadyExists) {
		t.Fatalf("Open while claimed error = %v, want already_exists", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	reopened, err := repository.Open("memory-session", Options{})
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	entries := reopened.storage.Entries()
	if len(entries) != 1 || entries[0].ID != "entry-1" || entries[0].Message == nil || entries[0].Message.Parts[0].Text != "hello" {
		t.Fatalf("reopened entries = %+v", entries)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened session: %v", err)
	}
	if _, err := repository.Create(Header{ID: "memory-session"}, Options{}); !hasSessionErrorCode(err, ErrorAlreadyExists) {
		t.Fatalf("duplicate Create error = %v, want already_exists", err)
	}
}

func TestMemoryRepositoryListIsStableAndDetached(t *testing.T) {
	repository := NewMemoryRepository()
	for _, header := range []Header{
		{ID: "later-z", CreatedAt: 20},
		{ID: "first", CreatedAt: 10},
		{ID: "later-a", CreatedAt: 20},
	} {
		session, err := repository.Create(header, Options{})
		if err != nil {
			t.Fatalf("Create(%s): %v", header.ID, err)
		}
		if err := session.Close(); err != nil {
			t.Fatalf("Close(%s): %v", header.ID, err)
		}
	}

	headers, err := repository.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got, want := headerIDs(headers), []string{"first", "later-a", "later-z"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("List ids = %v, want %v", got, want)
	}
	headers[0].ID = "mutated"
	again, err := repository.List()
	if err != nil {
		t.Fatalf("second List: %v", err)
	}
	if got := headerIDs(again); got[0] != "first" {
		t.Fatalf("List returned aliased headers: %v", got)
	}
}

func TestJSONLRepositoryReopensAndListsWithoutClaiming(t *testing.T) {
	root := t.TempDir()
	repository, err := NewJSONLRepository(root)
	if err != nil {
		t.Fatalf("NewJSONLRepository: %v", err)
	}
	session, err := repository.Create(
		Header{ID: "disk-session", CreatedAt: 10},
		Options{IDGenerator: sequenceIDGenerator("entry-1")},
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := session.AppendMessage(MainLane, agent.NewUserTextMessage("persisted")); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "disk-session.jsonl")); err != nil {
		t.Fatalf("session file: %v", err)
	}

	headers, err := repository.List()
	if err != nil {
		t.Fatalf("List active session: %v", err)
	}
	if got := headerIDs(headers); !reflect.DeepEqual(got, []string{"disk-session"}) {
		t.Fatalf("List ids = %v", got)
	}
	if _, err := repository.Open("disk-session", Options{}); !hasSessionErrorCode(err, ErrorAlreadyExists) {
		t.Fatalf("Open after List while claimed error = %v, want already_exists", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := repository.Open("disk-session", Options{})
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	entries := reopened.storage.Entries()
	if len(entries) != 1 || entries[0].Message == nil || entries[0].Message.Parts[0].Text != "persisted" {
		t.Fatalf("reopened entries = %+v", entries)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened session: %v", err)
	}
	if _, err := repository.Create(Header{ID: "disk-session"}, Options{}); !hasSessionErrorCode(err, ErrorAlreadyExists) {
		t.Fatalf("duplicate Create error = %v, want already_exists", err)
	}
}

func TestJSONLRepositoryListOrdersHeadersAndIgnoresOtherFiles(t *testing.T) {
	repository, err := NewJSONLRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONLRepository: %v", err)
	}
	for _, header := range []Header{
		{ID: "later-z", CreatedAt: 20},
		{ID: "first", CreatedAt: 10},
		{ID: "later-a", CreatedAt: 20},
	} {
		session, err := repository.Create(header, Options{})
		if err != nil {
			t.Fatalf("Create(%s): %v", header.ID, err)
		}
		if err := session.Close(); err != nil {
			t.Fatalf("Close(%s): %v", header.ID, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repository.root, "README.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatalf("write unrelated file: %v", err)
	}

	headers, err := repository.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got, want := headerIDs(headers), []string{"first", "later-a", "later-z"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("List ids = %v, want %v", got, want)
	}
}

func TestJSONLRepositoryListReadsOnlyHeader(t *testing.T) {
	repository, err := NewJSONLRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONLRepository: %v", err)
	}
	content := "{\"kind\":\"session\",\"version\":1,\"id\":\"header-only\",\"created_at\":1}\nnot-json\n"
	if err := os.WriteFile(filepath.Join(repository.root, "header-only.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatalf("write JSONL fixture: %v", err)
	}

	headers, err := repository.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := headerIDs(headers); !reflect.DeepEqual(got, []string{"header-only"}) {
		t.Fatalf("List ids = %v", got)
	}
	if _, err := repository.Open("header-only", Options{}); !hasSessionErrorCode(err, ErrorCorruptLog) {
		t.Fatalf("Open malformed tail error = %v, want corrupt_log", err)
	}
	if _, err := repository.Open("header-only", Options{}); !hasSessionErrorCode(err, ErrorCorruptLog) {
		t.Fatalf("second Open malformed tail error = %v, want corrupt_log instead of retained claim", err)
	}
}

func TestJSONLRepositoryOpenRejectsSymlink(t *testing.T) {
	parent := t.TempDir()
	repository, err := NewJSONLRepository(filepath.Join(parent, "sessions"))
	if err != nil {
		t.Fatalf("NewJSONLRepository: %v", err)
	}
	outside := filepath.Join(parent, "outside.jsonl")
	content := "{\"kind\":\"session\",\"version\":1,\"id\":\"linked\",\"created_at\":1}\n"
	if err := os.WriteFile(outside, []byte(content), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	link := filepath.Join(repository.root, "linked.jsonl")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks are unavailable in this environment: %v", err)
	}

	if _, err := repository.Open("linked", Options{}); !hasSessionErrorCode(err, ErrorStorage) {
		t.Fatalf("Open symlink error = %v, want storage", err)
	}
	headers, err := repository.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(headers) != 0 {
		t.Fatalf("List included symlink: %+v", headers)
	}
}

func TestRepositoriesRejectUnsafeSessionIDs(t *testing.T) {
	jsonl, err := NewJSONLRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONLRepository: %v", err)
	}
	repositories := []Repository{NewMemoryRepository(), jsonl}
	for _, repository := range repositories {
		for _, id := range []string{"../escape", `..\escape`, "/absolute", "C:volume", ".", "-leading"} {
			if _, err := repository.Create(Header{ID: id}, Options{}); !hasSessionErrorCode(err, ErrorInvalidHeader) {
				t.Errorf("Create(%q) error = %v, want invalid_header", id, err)
			}
			if _, err := repository.Open(id, Options{}); !hasSessionErrorCode(err, ErrorInvalidHeader) {
				t.Errorf("Open(%q) error = %v, want invalid_header", id, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(jsonl.root), "escape.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe id created a file outside root: %v", err)
	}
}

func TestRepositoryAllowsOnlyOneConcurrentWriter(t *testing.T) {
	jsonl, err := NewJSONLRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONLRepository: %v", err)
	}
	for name, repository := range map[string]Repository{
		"memory": NewMemoryRepository(),
		"jsonl":  jsonl,
	} {
		t.Run(name, func(t *testing.T) {
			created, err := repository.Create(Header{ID: "concurrent", CreatedAt: 1}, Options{})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if err := created.Close(); err != nil {
				t.Fatalf("Close created: %v", err)
			}

			const attempts = 16
			start := make(chan struct{})
			results := make(chan *Session, attempts)
			errorsCh := make(chan error, attempts)
			var waitGroup sync.WaitGroup
			for range attempts {
				waitGroup.Add(1)
				go func() {
					defer waitGroup.Done()
					<-start
					session, err := repository.Open("concurrent", Options{})
					if err != nil {
						errorsCh <- err
						return
					}
					results <- session
				}()
			}
			close(start)
			waitGroup.Wait()
			close(results)
			close(errorsCh)

			var winner *Session
			for session := range results {
				if winner != nil {
					t.Fatal("more than one concurrent Open succeeded")
				}
				winner = session
			}
			if winner == nil {
				t.Fatal("no concurrent Open succeeded")
			}
			var alreadyExists int
			for err := range errorsCh {
				if !hasSessionErrorCode(err, ErrorAlreadyExists) {
					t.Errorf("Open error = %v, want already_exists", err)
				}
				alreadyExists++
			}
			if alreadyExists != attempts-1 {
				t.Fatalf("already_exists count = %d, want %d", alreadyExists, attempts-1)
			}
			if err := winner.Close(); err != nil {
				t.Fatalf("Close winner: %v", err)
			}
			afterClose, err := repository.Open("concurrent", Options{})
			if err != nil {
				t.Fatalf("Open after winner Close: %v", err)
			}
			if err := afterClose.Close(); err != nil {
				t.Fatalf("Close afterClose: %v", err)
			}
		})
	}
}

func TestRepositoryStorageReleasesClaimAfterCloseError(t *testing.T) {
	storage := &repositoryStorage{
		Storage: &errorCloseStorage{Storage: mustMemoryStorage(t), err: errors.New("close failed")},
	}
	var releases atomic.Int64
	storage.release = func() { releases.Add(1) }
	if err := storage.Close(); err == nil {
		t.Fatal("Close error = nil")
	}
	if err := storage.Close(); err == nil {
		t.Fatal("second Close error = nil")
	}
	if got := releases.Load(); got != 1 {
		t.Fatalf("release count = %d, want 1", got)
	}
}

type errorCloseStorage struct {
	Storage
	err error
}

func (s *errorCloseStorage) Close() error {
	_ = s.Storage.Close()
	return s.err
}

func mustMemoryStorage(t *testing.T) Storage {
	t.Helper()
	storage, err := NewMemoryStorage(Header{ID: "close-error"})
	if err != nil {
		t.Fatalf("NewMemoryStorage: %v", err)
	}
	return storage
}

func sequenceIDGenerator(ids ...string) IDGenerator {
	var index atomic.Uint64
	return func() string {
		position := index.Add(1) - 1
		if position >= uint64(len(ids)) {
			return "exhausted"
		}
		return ids[position]
	}
}

func headerIDs(headers []Header) []string {
	ids := make([]string, len(headers))
	for index, header := range headers {
		ids[index] = header.ID
	}
	return ids
}

func hasSessionErrorCode(err error, code ErrorCode) bool {
	var sessionErr *Error
	return errors.As(err, &sessionErr) && sessionErr.Code == code
}
