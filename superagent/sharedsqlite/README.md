# Same-host shared journal

This bundled adapter supplies an exclusively leased journal to
`superagent.OpenMainJournal`. It supports competing processes on one host using
a local filesystem with reliable OS file locks. SQLite transactions and durable
fencing increments serialize ownership. The lease covers an entire host-owned
authorization domain. Parallel tasks run inside that one owner. `OpenMain`
provides the single-process JSONL API.

PostgreSQL and other shared backends belong to the embedding host. Implement
the public [Journal and LeasedJournal contracts](../journal.go) and supply the
claimed journal to `OpenMainJournal`. The host owns backend dependencies and validation.

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

Supported runtime targets are Linux/macOS/Windows amd64/arm64. Repository merge
review exercised Linux and Windows amd64. Use a same-host local disk with the
native OS VFS. Network filesystems, multi-host access, alternative VFS and
`sqlite3_dotlk` builds are unsupported. Unix database files are private 0600
and require a private parent directory. Windows needs equivalent host-managed ACLs. External effects
remain subject to the coordinator's existing review and reconciliation barriers.
On Windows use a drive-qualified path on a local disk. UNC and device namespace
paths are rejected before accessing the filesystem.
