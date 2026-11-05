//go:build sqlite3_dotlk

package sharedsqlite

// The adapter's cross-process acceptance tests exercise the native OS VFS.
// Do not silently substitute the driver's alternate dot-file lock protocol.
const supportedLocking = false
