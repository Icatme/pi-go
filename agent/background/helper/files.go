package helper

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// 256 bounded receipt identities may expand sixfold under JSON escaping.
// This also covers the 64 KiB output and immutable request envelope.
const maxJSONBytes = 16 << 20

// The entire ancestor chain, binary and registry must be host-owned and must
// not be concurrently replaced. These checks are not an adversarial filesystem
// sandbox or a defense against code running as the same operating-system user.
func privateDir(path string) error {
	i, e := os.Lstat(path)
	if e != nil || !i.IsDir() || i.Mode().Perm()&0077 != 0 {
		return ErrUnavailable
	}
	return nil
}
func privateFile(path string) error {
	i, e := os.Lstat(path)
	if e != nil || !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 {
		return ErrUnavailable
	}
	return nil
}
func executableDigest(path string) (string, error) {
	i, e := os.Lstat(path)
	if e != nil || !i.Mode().IsRegular() || i.Mode().Perm()&0022 != 0 || i.Mode().Perm()&0111 == 0 {
		return "", ErrInvalid
	}
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func strictJSON(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	return nil
}
func readJSON(path string, out any) error {
	if privateFile(path) != nil {
		return ErrUnavailable
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxJSONBytes+1))
	if err != nil || len(raw) > maxJSONBytes {
		return ErrUnavailable
	}
	return strictJSON(raw, out)
}
func writeNew(path string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(raw)
	return errors.Join(writeErr, f.Sync(), f.Close(), syncDir(filepath.Dir(path)))
}
func replaceJSON(path string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".observation-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(raw)
	if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
