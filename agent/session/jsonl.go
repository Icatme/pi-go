package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// JSONLStorage stores one session in one append-only file.
type JSONLStorage struct {
	mu       sync.RWMutex
	path     string
	file     *os.File
	writer   *bufio.Writer
	syncFile func() error
	state    *logState
	closed   bool
	failed   error
	closeErr error
}

// CreateJSONLStorage creates a new session file. It never overwrites an
// existing file. The header is flushed and synced before this function returns.
func CreateJSONLStorage(path string, header Header) (*JSONLStorage, error) {
	normalized, err := normalizeNewHeader(header)
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, sessionError(ErrorAlreadyExists, fmt.Sprintf("session file already exists: %s", path), err)
		}
		return nil, sessionError(ErrorStorage, fmt.Sprintf("create session file %s", path), err)
	}
	storage := &JSONLStorage{
		path:     path,
		file:     file,
		writer:   bufio.NewWriter(file),
		syncFile: file.Sync,
		state:    newLogState(normalized),
	}
	if err := storage.writeSynced(normalized); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return storage, nil
}

// OpenJSONLStorage validates and opens an existing session for append. It
// repairs only a syntactically torn final JSON fragment without a newline. A
// complete unterminated item, malformed line, sequence gap, or invalid parent
// is rejected.
func OpenJSONLStorage(path string) (*JSONLStorage, error) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, sessionError(ErrorNotFound, fmt.Sprintf("session file does not exist: %s", path), err)
		}
		return nil, sessionError(ErrorStorage, fmt.Sprintf("open session file %s", path), err)
	}
	return openJSONLStorageFile(path, file)
}

// openJSONLStorageFile takes ownership of an already opened handle.
// Repositories use it only after verifying the handle still identifies the
// regular file inspected inside their root, before replay may repair a tail.
func openJSONLStorageFile(path string, file *os.File) (*JSONLStorage, error) {
	header, state, err := loadJSONL(file, path)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		_ = file.Close()
		return nil, sessionError(ErrorStorage, fmt.Sprintf("seek session append position %s", path), err)
	}
	state.header = header
	return &JSONLStorage{
		path:     path,
		file:     file,
		writer:   bufio.NewWriter(file),
		syncFile: file.Sync,
		state:    state,
	}, nil
}

// ReadJSONLHeader reads only the immutable header and does not acquire or retain
// a writer handle. It is suitable for repository listing.
func ReadJSONLHeader(path string) (Header, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Header{}, sessionError(ErrorNotFound, fmt.Sprintf("session file does not exist: %s", path), err)
		}
		return Header{}, sessionError(ErrorStorage, fmt.Sprintf("open session header %s", path), err)
	}
	defer file.Close()
	return readJSONLHeaderFile(path, file)
}

func readJSONLHeaderFile(path string, file *os.File) (Header, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Header{}, sessionError(ErrorStorage, fmt.Sprintf("seek session header %s", path), err)
	}
	line, readErr := bufio.NewReader(file).ReadBytes('\n')
	if readErr != nil {
		if errors.Is(readErr, io.EOF) && len(line) != 0 {
			return Header{}, corruptLine(path, 1, "header is not newline terminated", nil)
		}
		if errors.Is(readErr, io.EOF) {
			return Header{}, corruptLine(path, 1, "header is missing", nil)
		}
		return Header{}, sessionError(ErrorStorage, fmt.Sprintf("read session header %s", path), readErr)
	}
	return decodeHeaderLine(path, bytes.TrimSuffix(line, []byte{'\n'}))
}

// Path returns the backing file path.
func (s *JSONLStorage) Path() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.path
}

func (s *JSONLStorage) Header() Header {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.headerClone()
}

func (s *JSONLStorage) Lanes() []LanePointer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.lanesClone()
}

func (s *JSONLStorage) CreateLane(lane, at string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireWritable(); err != nil {
		return err
	}
	item, err := s.state.newLaneItem(lane, at, true)
	if err != nil {
		return err
	}
	return s.appendItem(item)
}

func (s *JSONLStorage) MoveLane(lane, to string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireWritable(); err != nil {
		return err
	}
	item, err := s.state.newLaneItem(lane, to, false)
	if err != nil {
		return err
	}
	return s.appendItem(item)
}

func (s *JSONLStorage) AppendEntry(lane string, input NewEntry) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireWritable(); err != nil {
		return Entry{}, err
	}
	item, err := s.state.newEntryItem(lane, input)
	if err != nil {
		return Entry{}, err
	}
	if err := s.appendItem(item); err != nil {
		return Entry{}, err
	}
	return mustCloneJSON(*item.Entry), nil
}

func (s *JSONLStorage) Entry(id string) (Entry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.entryClone(id)
}

func (s *JSONLStorage) Entries() []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.entriesClone()
}

func (s *JSONLStorage) Log() []LogItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.logClone()
}

// Close flushes, syncs, and closes the backing file. It is idempotent.
func (s *JSONLStorage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	flushErr := s.writer.Flush()
	syncErr := s.syncFile()
	closeErr := s.file.Close()
	if err := errors.Join(s.failed, flushErr, syncErr, closeErr); err != nil {
		s.closeErr = sessionError(ErrorStorage, fmt.Sprintf("close session file %s", s.path), err)
	}
	return s.closeErr
}

func (s *JSONLStorage) appendItem(item LogItem) error {
	if err := s.writeSynced(item); err != nil {
		s.failed = err
		return err
	}
	if err := s.state.apply(item); err != nil {
		// The exact validated item was just synced. Reaching this branch means an
		// internal invariant failed, so poison the writer instead of risking a
		// second item with the same sequence.
		s.failed = err
		return sessionError(ErrorStorage, "synced session item could not be applied", err)
	}
	return nil
}

func (s *JSONLStorage) writeSynced(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return sessionError(ErrorStorage, "encode session JSONL item", err)
	}
	encoded = append(encoded, '\n')
	start, err := s.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return sessionError(ErrorStorage, fmt.Sprintf("locate session append position %s", s.path), err)
	}
	if _, err := s.writer.Write(encoded); err != nil {
		return s.rollbackFailedWrite(start, "append", err)
	}
	if err := s.writer.Flush(); err != nil {
		return s.rollbackFailedWrite(start, "flush", err)
	}
	if err := s.syncFile(); err != nil {
		return s.rollbackFailedWrite(start, "sync", err)
	}
	return nil
}

func (s *JSONLStorage) rollbackFailedWrite(start int64, stage string, writeErr error) error {
	// Discard any bytes retained by bufio before restoring the last synced
	// prefix. The writer remains poisoned even when rollback succeeds.
	s.writer.Reset(s.file)
	truncateErr := s.file.Truncate(start)
	_, seekErr := s.file.Seek(start, io.SeekStart)
	syncErr := s.syncFile()
	cause := errors.Join(writeErr, truncateErr, seekErr, syncErr)
	return sessionError(ErrorStorage, fmt.Sprintf("%s session file %s", stage, s.path), cause)
}

func (s *JSONLStorage) requireWritable() error {
	if s.closed {
		return sessionError(ErrorStorage, "session storage is closed", nil)
	}
	if s.failed != nil {
		return sessionError(ErrorStorage, "session storage is unavailable after an append failure", s.failed)
	}
	return nil
}

func loadJSONL(file *os.File, path string) (Header, *logState, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Header{}, nil, sessionError(ErrorStorage, fmt.Sprintf("seek session file %s", path), err)
	}
	reader := bufio.NewReader(file)
	headerLine, err := readRequiredLine(reader, path, 1)
	if err != nil {
		return Header{}, nil, err
	}
	header, err := decodeHeaderLine(path, headerLine)
	if err != nil {
		return Header{}, nil, err
	}
	state := newLogState(header)
	validEnd := int64(len(headerLine) + 1)
	for lineNumber := 2; ; lineNumber++ {
		line, readErr := reader.ReadBytes('\n')
		if errors.Is(readErr, io.EOF) {
			if len(line) == 0 {
				break
			}
			var item LogItem
			decodeErr := decodeStrictJSON(line, &item)
			if isJSONSyntaxError(decodeErr) {
				if err := file.Truncate(validEnd); err != nil {
					return Header{}, nil, sessionError(ErrorStorage, fmt.Sprintf("truncate torn session tail %s", path), err)
				}
				if err := file.Sync(); err != nil {
					return Header{}, nil, sessionError(ErrorStorage, fmt.Sprintf("sync repaired session file %s", path), err)
				}
				break
			}
			if decodeErr != nil {
				return Header{}, nil, corruptLine(path, lineNumber, "malformed log item", decodeErr)
			}
			return Header{}, nil, corruptLine(path, lineNumber, "complete log item is not newline terminated", nil)
		}
		if readErr != nil {
			return Header{}, nil, sessionError(ErrorStorage, fmt.Sprintf("read session file %s", path), readErr)
		}
		line = bytes.TrimSuffix(line, []byte{'\n'})
		if len(line) == 0 {
			return Header{}, nil, corruptLine(path, lineNumber, "empty line", nil)
		}
		var item LogItem
		if err := decodeStrictJSON(line, &item); err != nil {
			return Header{}, nil, corruptLine(path, lineNumber, "malformed log item", err)
		}
		if err := state.apply(item); err != nil {
			return Header{}, nil, corruptLine(path, lineNumber, "invalid log item", err)
		}
		validEnd += int64(len(line) + 1)
	}
	return header, state, nil
}

func isJSONSyntaxError(err error) bool {
	if err == nil {
		return false
	}
	var syntaxError *json.SyntaxError
	return errors.As(err, &syntaxError) || errors.Is(err, io.ErrUnexpectedEOF)
}

func readRequiredLine(reader *bufio.Reader, path string, lineNumber int) ([]byte, error) {
	line, err := reader.ReadBytes('\n')
	if err == nil {
		line = bytes.TrimSuffix(line, []byte{'\n'})
		if len(line) == 0 {
			return nil, corruptLine(path, lineNumber, "line is empty", nil)
		}
		return line, nil
	}
	if errors.Is(err, io.EOF) && len(line) != 0 {
		return nil, corruptLine(path, lineNumber, "line is not newline terminated", nil)
	}
	if errors.Is(err, io.EOF) {
		return nil, corruptLine(path, lineNumber, "line is missing", nil)
	}
	return nil, sessionError(ErrorStorage, fmt.Sprintf("read session file %s", path), err)
}

func decodeHeaderLine(path string, line []byte) (Header, error) {
	var header Header
	if err := decodeStrictJSON(line, &header); err != nil {
		return Header{}, corruptLine(path, 1, "malformed header", err)
	}
	if err := validateHeader(header); err != nil {
		return Header{}, corruptLine(path, 1, "invalid header", err)
	}
	return header, nil
}

func decodeStrictJSON(line []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func corruptLine(path string, line int, reason string, cause error) error {
	return sessionError(ErrorCorruptLog, fmt.Sprintf("invalid session file %s at line %d: %s", path, line, reason), cause)
}

var _ Storage = (*JSONLStorage)(nil)
