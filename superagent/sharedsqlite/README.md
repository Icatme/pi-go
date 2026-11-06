# Same-host shared journal

This optional adapter supplies an exclusively leased journal to
`superagent.OpenMainJournal`. It uses SQLite transactions and durable fencing
increments, not an in-memory mutex, to serialize competing processes. The lease
covers an entire host-owned authorization domain. Parallel tasks run inside that
one owner. Use `OpenMain` for the unchanged single-process JSONL API.

Create a trusted local directory with mode 0700 and open a database inside it:

```go
journal, err := sharedsqlite.Open(ctx, databasePath, sharedsqlite.Options{
    Domain: "workspace-A",
    Owner: "instance-unique-id",
    LeaseTTL: 30 * time.Second,
})
```

`ErrBusy` means another valid owner exists. A successful open starts renewal.
Pass the journal to `OpenMainJournal`; after argument validation the coordinator
owns it and will close it even if replay fails. If you do not transfer ownership,
call `journal.Close()` yourself. Cancellation or uncertain storage failures end
that journal's authority. Reopen and replay rather than retrying effects.

`ImportJSONL` atomically imports an old v2 JSONL log into a previously absent
SQLite domain. Keep legacy writers stopped through migration and cutover. The
source is unchanged; single-item and transaction-batch logs are supported.

See [M6a](../../docs/superagent-m6a.md) for driver evidence, security requirements,
clock/expiry semantics, migration rules, verification and remaining scope.
The [external-package example](../shared_journal_example_test.go) is executable.

Supported runtime targets are Linux/macOS/Windows amd64/arm64, with Linux amd64
actually tested here. No network filesystems, multi-host use, alternative VFS or
`sqlite3_dotlk` builds. Unix database files are private 0600 and require a private
parent directory; Windows needs equivalent host-managed ACLs. External effects
remain subject to the coordinator's existing review and reconciliation barriers.
On Windows use a drive-qualified path on a local disk. UNC and device namespace
paths are rejected before accessing the filesystem.
