package session

import (
	"errors"
	"fmt"
	"os"
)

// ReadJSONLSnapshot reads a validated, detached header and ordered log while
// holding the same exclusive lock as a JSONL writer. It never repairs a torn
// tail or changes the source. Batch records are flattened into their original
// ordered LogItems. Missing, malformed or incomplete files are errors.
//
// The lock is released before return. Migration callers must keep old writers
// quiesced until the destination commits and their cutover is complete.
func ReadJSONLSnapshot(path string) (header Header, log []LogItem, err error) {
	file, err := os.Open(path)
	if err != nil {
		code := ErrorStorage
		if errors.Is(err, os.ErrNotExist) {
			code = ErrorNotFound
		}
		return Header{}, nil, sessionError(code, fmt.Sprintf("open session snapshot %s", path), err)
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return Header{}, nil, sessionError(ErrorStorage, "inspect session snapshot", err)
	}
	if !info.Mode().IsRegular() {
		return Header{}, nil, sessionError(ErrorStorage, "session snapshot must be a regular file", nil)
	}
	if err = lockSessionFile(file); err != nil {
		return Header{}, nil, err
	}
	header, state, err := loadJSONLMode(file, path, false)
	if err != nil {
		return Header{}, nil, err
	}
	return header, state.logClone(), nil
}
