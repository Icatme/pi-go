# Optional filesystem tools

`fstools.Definitions(root, options)` creates one ordinary Agent definition:
`ls`. `fstools.ReadDefinition(root, readOptions)` separately opts into `read`;
`fstools.FindDefinition(root, findOptions)` separately opts into filename search.
`fstools.GrepDefinition(root, grepOptions)` opts into single-file content search.
`fstools.CreateFileDefinition(root, createOptions)` explicitly opts into new-file
creation only. `fstools.PreviewEditDefinition(root, previewOptions)` opts into
read-only replacement previews. `fstools.AtomicReplaceDefinition(root, replaceOptions)`
explicitly opts into ordinary create-or-replace workspace writes.
`fstools.EditDefinition(root, editOptions)` separately opts into exact replacement
and actual publication. Existing `Definitions` callers never gain these
capabilities by upgrading.
Nothing is installed or enabled in the core Agent, CLI, managed MCP configuration,
or a downstream application. This is not a complete filesystem suite:
shell and Python are not supplied.

Use each argument name exactly as spelled in the tool schema and only once per
JSON object. The tools reject unknown or repeated names before filesystem access.

```go
// trustedWorkspace comes from application configuration, never model input.
root, err := os.OpenRoot(trustedWorkspace)
if err != nil { return err }
defer root.Close() // Keep open until all tool calls finish.
tools, err := fstools.Definitions(root, fstools.Options{
    MaxEntries: 256,
    MaxResultBytes: 64 << 10,
})
if err != nil { return err }
definition.Tools = append(definition.Tools, tools...)
// Set definition.CheckToolPermission to your current authorization policy.
```

The factory does no I/O and does not own or close the supplied `*os.Root`.
Create a separate root and tool set for each trusted tenant/session authorization
scope. A manager's MCP identity changes do not automatically rebind native
filesystem tools. The host controls scope changes and revocation; it should
retire old definitions and stop calls before closing their root. A nil permission
hook means the host permits directory listings under that root and, when
`ReadDefinition` is explicitly selected, the text reads that tool supports;
selecting `FindDefinition` similarly authorizes its bounded filename traversal.

## Explicit fixed presets

For one-line assembly, use either of these **opt-in** factories:

```go
readOnly, err := fstools.ReadOnlyDefinitions(root, fstools.ReadOnlyOptions{})
// Exact order: ls, read, find, grep, preview_edit.

workspace, err := fstools.WorkspaceWriteDefinitions(root, fstools.WorkspaceWriteOptions{})
// Exact order: the five read tools, create_file, atomic_replace, edit.
```

The workspace preset is a **read-and-write** capability, including replacement
of existing files. It is not a create-only preset. `ReadOnlyOptions` has per-tool
`List`, `Read`, `Find`, `Grep`, `Preview` limits; `WorkspaceWriteOptions` nests those
under `ReadOnly` and adds `Create`, `Replace`, `Edit` limits. Zero fields retain the
individual tools' bounded defaults. Limits apply to each tool call, not a shared
per-tenant or multi-call budget. A host must impose any aggregate budget itself.

Membership is fixed and tested: neither preset silently gains future tools.
`Definitions` continues returning only `ls`. All factories return fresh definitions
and schemas, perform no filesystem I/O and leave root ownership with the caller.
Any invalid member returns `nil, error`, not a partial set. Neither preset enables
core Agent, CLI, MCP manager or downstream application defaults. Unsupported
platforms fail closed; the underlying Linux/procfs/O_TMPFILE and trusted-workspace
requirements still apply when the relevant tools execute.

Read-only does not mean redacted or safe to share: read, grep and preview may
return sensitive contents. Workspace writes retain last-writer-wins semantics,
new-inode metadata changes and malicious-directory-writer restrictions documented
below. No shared mutation queue, CAS, OS sandbox, shell or Python is introduced.
Use a separate root and preset per authorization scope; managed MCP scopes do not
rebind native filesystem closures.

The returned slice is ordinary `[]agent.ToolDefinition`. Hosts may explicitly
filter it or append other separately authorized definitions. For read plus
create-only access, compose `ReadOnlyDefinitions` and `CreateFileDefinition`; do
not select the full workspace preset and assume it forbids overwrites. Callers
must check all constructor errors before installing tools.

[The Go examples](example_test.go) show direct versus Codemode assembly, per-member
`codemodetool.Native` bindings, verified `ExportedName()` aliases, and the same
`CheckToolPermission` hook for both the outer code call and actual leaf tool names.
These examples are compiled but not run automatically: their workspace paths are
host configuration placeholders. Separate integration tests execute the adapter
with temporary fixtures, including outer/leaf denial and a read-only set that
cannot invoke edit. Allowing the outer code tool never implicitly authorizes its
leaf calls. Do not bypass the lifecycle by directly calling `Execute` for untrusted
model requests without the host's checks.

## Write execution and receipt requirements

The write definitions (`create_file`, `atomic_replace`, `edit`) require a live,
reserved receipt slot **before filesystem I/O**, including edit's source read.
A preset installs capabilities, not capture or authorization. Raw `Execute`, old
`RunToolCall`, ordinary legacy Runner/Engine and session model/checkpoint paths
cannot silently perform these writes without capture. Read-only tools keep their
existing behavior and do not require receipts.

Supported explicit host paths are:

- `agent.RunToolCallWithReceipts` for a direct call, including Codemode children.
- `Runner.RunWithReceipts(ctx, snapshot, prompts, hooks, maxCalls)` for an ordinary
  **non-persistent** model run; `hooks.Journal` must be nil. Drain the returned
  RunStream's Events before Wait, then retain the separate receipt handle.
- [Session-backed direct calls](../session/RECEIPTS.md) for an acknowledged
  intent/final protocol with an old-runner fence. This is not a persistent
  multi-turn model runner or checkpoint resume mechanism.

Use the returned handle's immutable snapshots to inspect local commits. A false
Committed flag means unknown, never proof of no effects. After cancellation,
actual host bodies can still be active; retain the handle, root and authorization
resources until they have been reconciled. The same live session binding can
Finalize late completion; a crashed pending session cannot simply be cleared.

These are host-side receipts, **not `Execution.LocalEffect` fields** in old
results, child reports, JSON or JavaScript errors. Output hooks cannot erase the
handle's facts, but old consumers do not acquire them automatically. The
in-process receipts are not crash-durable. Session durability depends on Storage,
and external file publication and receipt append are not one transaction.

**Capture does not stop repeated calls.** Models and JavaScript may call again
after failure, including after a commit. Neither receipt collection nor a file
tool supplies idempotency, a retry barrier, backup, CAS or automatic write safety.
Hosts must explicitly choose authorization and retry policies; observing events
or an ordinary permission callback is not automatically a race-free stop rule.
Filesystem publication itself is not retried by these tools.

See [receipt execution](../../executionreceipt/README.md) for capacity, pending
lifetime, immutable views and the deliberate JSON-encoding restrictions.

## Directory-listing contract and limits

`ls({"path":"."})` lists the root. A path is required, valid Unicode, at most
4096 UTF-8 bytes, relative and slash-separated. Absolute paths, any `..`
component, NUL, backslashes and colons fail. For `ls`, relative symlinks are allowed only
when Go's `os.Root` resolves them within the opened root; escaping and absolute
symlink targets fail, including absolute targets that happen to point inside.
The result contains `entries: [{name, type}]` and `truncated: bool`. Types are
`file`, `directory`, `symlink`, or `other`; no file contents, symlink target text,
absolute host paths or per-file stat details are returned.

Entries are in filesystem order, not sorted. This is a bounded, nonrecursive
listing, not a stable snapshot or paging API. Concurrent directory mutations may
change the observed entries. Listing reads at most `MaxEntries + 1` entries,
one at a time, and stops when the encoded result cap would be exceeded. Go may
buffer directory bytes internally. The default entry limit is 256 (maximum
4096). The JSON envelope, commas and escaped filenames count against the result
limit, default 64 KiB (minimum 256 bytes, maximum 1 MiB). The same JSON is returned
as text and structured content, so both retained representations together can
consume twice the configured payload bound, plus runtime metadata. These limits
apply to executor-produced output; trusted after hooks must bound their own
replacement data, or the host can install a stricter result validator. Non-UTF-8
filenames fail rather than being silently replaced. Truncation can return no
entries if even the first name would not fit; increasing a host limit is the
only way to list that name through this increment.

Arguments are checked both by parsing and immediately inside `Execute`, including
after hook changes. Permission is rechecked before opening, before each bounded
read, and before returning data. Cancellation is checked at those boundaries.
No background goroutine is spawned to pretend a blocked syscall was canceled.
A context deadline **cannot guarantee interruption of an in-flight filesystem
syscall**, especially on network/FUSE filesystems. This package is intended for
host-selected local directories, not hostile filesystem servers.

## Platform and security boundary

This increment supports **Linux only**. On other GOOS values, the factory fails
with `ErrUnsupportedPlatform` and exposes no tools; compiling a program on those
platforms does not mean execution is supported. Linux opens with
`O_DIRECTORY | O_NONBLOCK` through `os.Root.OpenFile`, so an attacker cannot turn
a checked directory into a FIFO or device between a `Stat` and an ordinary
`Open`. Merely checking a file type before opening would not provide that property.

`os.Root` supplies traversal/symlink containment, including concurrent symlink
replacement protection. This is **not OS process isolation** and does not stop
mount crossings, hard links, `/proc`, or authority from other application tools.
The host must select the root and its filesystem/mount content appropriately.
The initial `os.OpenRoot` call is the host's responsibility and should use a
trusted directory path. Applications needing a stronger boundary must provide
an independently configured OS/container sandbox. No isolation claim is made
for Windows, macOS, JS/WASM, or any untested filesystem/platform.

## Explicit text-file reads

```go
readTool, err := fstools.ReadDefinition(root, fstools.ReadOptions{
    MaxBytes: 64 << 10,
    MaxResultBytes: 128 << 10,
})
if err != nil { return err }
definition.Tools = append(definition.Tools, readTool)
```

`read({"path":"notes.txt"})` returns `{text: "..."}` as identical JSON text and
structured content. It accepts the same relative path syntax as `ls`, but rejects
**all final symlinks**, even an otherwise safe relative symlink. Intermediate
symlink directories are resolved by `os.Root` under its normal containment rules.
There is no offset, paging, model-controlled limit or partial/truncated text.

Only a regular inode can be read. Directories, FIFOs, devices, sockets and final
symlink inodes fail. NUL bytes and invalid UTF-8 fail without returning content.
The byte limit defaults to 64 KiB, maximum 1 MiB. Reads request at most the limit
plus one byte, in chunks of at most 8 KiB, so file growth cannot bypass the cap.
The encoded JSON limit defaults to 128 KiB, minimum 256 bytes and maximum 8 MiB;
escaping expansion and the envelope count. Exceeding either limit is an explicit
error, never a silent truncation. Retaining both result representations consumes
up to twice the encoded result bound; input buffers and runtime metadata are
additional. Trusted after hooks remain responsible for their replacement output.

### Read-open security boundary

Linux read first pins the target with `O_PATH` through `os.Root`, checks its inode
type, then reopens **that descriptor** through a pinned `/proc/self/fd` directory.
It never reopens the original untrusted pathname. The original descriptor stays
alive until the new read descriptor is verified to be the same regular inode.
This avoids a file-to-FIFO/device/symlink replacement between checking and opening.
OS read permissions still apply when the pinned inode is reopened. The original
pathname can be removed/replaced without redirecting the read to its replacement.
The file's contents can still change in place: this is not a snapshot or a lock.

This technique requires a **trusted host mount namespace and a normal procfs**
mounted at `/proc`; absent/unavailable procfs fails closed. The proc directory's
filesystem type and reopened inode identity are checked, but these checks do not
prove that a maliciously rebound proc subtree belongs to the current process.
A host that can rebind `/proc/self/fd` or manipulate the process's descriptor table
is outside this threat model. No claim of protection against such a host is made.

The host must select a workspace containing ordinary local data files. Regular
inode mode does **not** prove ordinary disk-file behavior: virtual filesystems may
expose regular-looking files with special semantics. Procfs and sysfs file targets
are rejected as defense in depth; this is not an exhaustive classifier of virtual
filesystems. Mount crossings, hardlinks, hostile/FUSE/network filesystem behavior
and arbitrary in-process code are not isolated. Do not expose virtual/hostile
filesystem roots expecting this tool to make them safe. Stronger requirements
need an independently configured OS/container boundary, not a broader tool label.

Permission/cancellation is checked before pinning, before accessing procfs,
immediately before reopening, before every read chunk and before publishing text.
No syscall is promised to obey a context deadline. Only errors with safe generic
messages reach the model; original OS causes remain inspectable by trusted Go
callers. Neither construction nor reading invokes a shell or a subprocess.

## Explicit filename discovery

```go
findTool, err := fstools.FindDefinition(root, fstools.FindOptions{
    MaxVisitedEntries: 10000,
    MaxDepth: 8,
    MaxMatches: 256,
    MaxResultBytes: 64 << 10,
})
if err != nil { return err }
definition.Tools = append(definition.Tools, findTool)
```

`find({"path":".", "pattern":"*.go"})` searches **basenames**, not file contents.
The pattern is a case-sensitive Go `path.Match` glob, at most 256 UTF-8 bytes:
`*`, `?`, character classes and backslash escaping are supported. A slash, NUL,
empty pattern or malformed glob fails before filesystem I/O. `**` has no special
recursive meaning; traversal happens independently of the basename match. A
nonmatching directory is still searched unless a traversal limit prevents it.

Results contain `entries: [{path, type}]`, `visited`, `truncated` and `limits`.
Types match `ls`. Paths are slash-separated, root-relative logical traversal paths;
the starting directory itself is not a match candidate. Basenames are preserved
exactly and JSON-escaped, including newlines, colons and backslashes. A discovered
path with a colon/backslash is not accepted by this package's stricter path-input
syntax; finding its name does not expand the read tool's accepted path syntax.
Non-UTF-8 names fail instead of undergoing lossy replacement.

Traversal is depth-first in filesystem order, not sorted, paged or a snapshot.
The starting directory is **depth 1**: `MaxDepth: 1` examines its entries but enters
no child directories. Matching directories at the boundary may be returned,
with `max_depth` recorded; other entries at the current level are still searched.
Every examined entry counts against `MaxVisitedEntries`, including nonmatches.
Entry and match budgets stop the search as soon as exhausted, without an extra
lookahead read to prove whether more data exists. Therefore `truncated: true`
means **completeness was not established**, not that omitted matches necessarily
exist. Reaching the depth bound on an empty child is conservative in the same way.

Defaults/ceilings are: visited entries 10000/100000, depth 8/64, matches 256/4096,
encoded result bytes 64 KiB/1 MiB (minimum 256). All are host-controlled. `limits`
contains any reached `max_depth`, `max_visited_entries`, `max_matches`, or
`max_result_bytes`. Space for the worst-case envelope and reasons is reserved
before collecting matches; this can stop before fully filling the byte cap.
Matching paths too large for the budget can produce an empty truncated result.
Both retained result representations together consume up to twice the JSON cap.
Trusted after hooks remain responsible for bounding their replacement results.
No full-directory or full-tree buffering occurs: each directory read requests one
entry, though Go internally buffers directory bytes/metadata. At most `MaxDepth`
directory handles are held by the traversal; all are closed on success/error.

The starting path uses `os.Root` containment and may resolve a safe in-root
symlink, as `ls` does. **Child symlinks are listed but never descended through.**
Each child open uses one basename relative to the still-open parent descriptor,
with `O_DIRECTORY | O_NOFOLLOW | O_NONBLOCK`; a directory swapped for a symlink,
FIFO or nondirectory cannot redirect that descent. File content is never
opened/read, and symlink target text is never returned. Other special entries
can be returned by name and type. Permission is checked before the initial open, every bounded directory
read, every child open and final publication. Cancellation/revocation or an I/O
error discards accumulated matches rather than returning an apparently complete
partial search. An empty read without EOF is also an error: a disappearing
entry must not be mistaken for a complete search. The same guard applies to
`ls`. Concurrent mutation may therefore cause a safe error.

A directory moved after it was opened remains the object being traversed. Its
reported logical path may no longer resolve from the root afterward. This is not
live ancestry enforcement or OS isolation: mount crossings/cycles, hostile
filesystems and arbitrary in-process code remain outside the security guarantee.
Depth/entry budgets bound work on successful operations but cannot interrupt a
blocked filesystem syscall. The host still selects a trusted local workspace and
uses the permission hook to revoke in-flight calls; closing the root alone is not
revocation of already-open directory descriptors. Linux only; no procfs dependency
is introduced for filename discovery.

## Single-file grep

`GrepDefinition` is a separate, explicit content-read capability. It reuses
`read`'s pinned-inode opener and bounded whole-file text reader, not a second
filesystem implementation. The same Linux/procfs, final-symlink, trusted local
workspace and syscall-cancellation restrictions apply. Other platforms fail
closed. No shell, ripgrep process, download or new module dependency is involved.

```go
grepTool, err := fstools.GrepDefinition(root, fstools.GrepOptions{
    MaxScanBytes: 64 << 10,
    MaxLineBytes: 16 << 10,
    MaxScannedLines: 10000,
    MaxMatches: 100,
    MaxResultBytes: 64 << 10,
})
// Check err, then explicitly add grepTool to the application's allowed tools.
```

Arguments are `{path, pattern}`. `path` must identify one file under the same
relative-path policy as `read`; directories and final symlinks are rejected.
This first increment is not recursive grep, has no glob/gitignore discovery,
context lines or pagination, and does not promise Pi/Eino API compatibility.
Use `find` separately for bounded filename discovery; the host must budget any
multi-file orchestration rather than assuming per-call limits bound a whole run.

Patterns use Go's case-sensitive regexp syntax (inline flags such as `(?i)`
are allowed), not PCRE/backreferences. A nonempty pattern is limited to 256 UTF-8
bytes. The implementation validates a conservative instruction-expansion cost
before simplification, then enforces a 4096-instruction compiled-program limit.
Valid but expensive patterns can be rejected. These bounds do not promise exact
allocator-byte limits or immediate cancellation during a regexp operation.
Matching uses the standard library, not a custom regexp engine.

The complete file must fit `MaxScanBytes` (default 64 KiB, ceiling 1 MiB), be valid
UTF-8 without NUL, and every line must fit `MaxLineBytes` (default 16 KiB, ceiling
256 KiB). Violations fail explicitly without partial content, even when an early
match would already fill the output. LF separates lines and is excluded from
line text/length; CR is retained, including in CRLF. A terminal LF does not add a
synthetic extra line. Empty files have zero lines; an LF by itself has one empty
line. UTF-8 line limits count bytes, not characters.

The result contains `matches: [{line, text}]` with 1-based line numbers and at
most one entry per matching line, `bytes_read` for the whole validated file,
`lines_scanned` for lines actually regex-tested, `truncated`, and `limits`.
Matching stops at `max_scanned_lines` (default 10000, ceiling 100000),
`max_matches` (default 100, ceiling 4096), or `max_result_bytes` (default 64 KiB,
range 256 bytes to 1 MiB). Encoded JSON escapes and result metadata count against
the byte budget. `truncated` conservatively means completeness was not proven;
an exact match-count boundary may set it even if nothing was omitted. No line
is silently shortened or skipped. Permissions/cancellation are checked during
reading, validation, between matching lines and before publishing; revocation
or cancellation discards all results.

Matching lines can contain credentials or other sensitive text. This is not
redaction; the host must approve content-read access and handle returned data
accordingly. Unmatched lines are not returned. The design borrows explicit
opt-in tools, matching-line/line-number results and visible output-limit semantics
from [Pi](https://github.com/earendil-works/pi/blob/9c9ce8547e5a88cbe834a53e9a6d83a789d08e47/packages/coding-agent/src/core/tools/grep.ts)
and [Eino](https://github.com/cloudwego/eino/blob/v0.9.1/adk/middlewares/filesystem/filesystem.go),
without importing their Agent runtimes or copying their subprocess/backend
implementations. File opening and permissions remain this package's narrower
contract; neither upstream's local backend is treated as an OS sandbox.

## Create-only text files

`CreateFileDefinition` exposes `create_file({path, content})` only when the host
explicitly selects it. It is not an overwriting `write`/`edit` tool. Existing
names of every type (including dangling symlinks, FIFOs, directories and
hardlinks) are never replaced. Parent directories must already exist; there is
no automatic mkdir, append, chmod option, or default write preset.

```go
createTool, err := fstools.CreateFileDefinition(root, fstools.CreateOptions{
    MaxBytes: 64 << 10,
})
// Check err and explicitly authorize/bind this definition when writes are wanted.
```

Content must be valid UTF-8 without NUL and fit the host byte limit (default
64 KiB, ceiling 1 MiB). Empty content is supported. The model cannot change
file permissions or overwrite policy. Paths use the same root-relative policy;
a trailing slash or `/.` is rejected rather than silently treated as a file.
New files have permission bits **at most 0600**, further restricted by the host's
umask. The tool does not change process-wide umask, preserve an existing file's
metadata or accept model-selected executable permissions.

On Linux with a supporting local filesystem, an existing parent directory is
opened through `os.Root` and kept fixed by descriptor. An anonymous `O_TMPFILE`
inode is written completely in bounded chunks and synced, then published using
`linkat` from a fixed, trusted proc fd directory to one basename under the same
parent descriptor. There is no named temporary file for another process to
swap. The kernel's new-link operation refuses existing names, including a name
created by a concurrent caller. Unsupported `O_TMPFILE`/procfs/platform cases
fail without fallback to truncation or a named temporary file. Directory sync
is requested after successful publication. No dependencies or external process
are added; the proc-directory helper is shared with `read`.

**Authority and concurrency boundary:** the host must choose a trusted local
workspace/filesystem and mount namespace, as for `read`. Once opened, authority
is to that fixed parent directory inode, not a continuously rechecked pathname
ancestry. The host must prevent unauthorized directory moves across roots and
hostile mount/proc changes. A replaced parent symlink cannot redirect the fixed
handle, but renaming the directory does not revoke it. Closing `os.Root` does
not revoke already-open handles; drain outstanding calls when changing scope.
The final permission/context check is immediately before publication, but the
check and syscall are not atomic with an external revocation. In-flight writes
and syncs cannot be guaranteed to stop immediately on cancellation.

Publication success is the commit point. The normal result is
`{created:true, durability:"synced"}`; `synced` means the requested file/directory
sync calls returned successfully, not a guarantee of honest hardware, continued
path identity or protection from another writer after publication. Once linked,
any later directory-sync/cleanup/cancellation/revocation failure retains
a committed fact in the host receipt handle, returns a safe failure, and may report
`durability:"unknown"`. An unexpected publication error is conservatively
`unknown`: it is not proof that no destination was created. A known existing-name
conflict is reported without opening or overwriting that object.

There is **no pathname unlink rollback** after attempting publication: another
writer may already have changed that name. Pre-publication failure closes the
anonymous file without leaving a named staging entry. Host receipt facts for actual leaf calls,
including calls through Codemode, survive output hooks and result rejection;
ordinary business JSON alone is not a receipt. These are not crash-durable
receipts or automatic idempotency: reconcile uncertain outcomes before another
attempt, and do not blindly retry or roll back a reported commit. No filesystem
operation is retried by this tool. The package does not claim a transaction,
mount isolation or a general OS sandbox.

## Read-only edit previews

`PreviewEditDefinition` exposes `preview_edit({path, old_text, new_text})`.
It reads one bounded file and computes a candidate in memory; it never modifies
the original, creates another file, or automatically invokes `create_file`.
The complete candidate can be reviewed by a person or separately passed to an
explicitly authorized create-only operation under a **new** destination name.
The separately authorized `edit` rereads the source and computes a new candidate;
it does not apply this preview or its fingerprint. Both `edit` and `atomic_replace`
use last-writer-wins publication, not guarded preview application.

```go
previewTool, err := fstools.PreviewEditDefinition(root, fstools.PreviewEditOptions{
    MaxBytes: 64 << 10,
    MaxResultBytes: 128 << 10,
})
// Check err and explicitly select/bind this content-reading capability.
```

The source, old text, new text and candidate each have a host byte limit
(default 64 KiB, ceiling 1 MiB). `old_text` must be nonempty and occur exactly
once, **including overlapping occurrences**: searching `aa` in `aaa` is
ambiguous. No match, multiple matches and identical old/new text are errors.
An empty `new_text` deletes the selected occurrence. Replacement text is
inserted once and is not searched/replaced recursively.

Matching is exact on the UTF-8 bytes, not fuzzy, case-insensitive or normalized.
The tool does not normalize BOMs, whitespace, LF, CRLF, mixed endings or bare CR.
Bytes outside the selected match stay unchanged. For a cross-line match, include
its actual newline characters in `old_text`; `a\nb` does not match `a\r\nb`.
A BOM is preserved unless explicitly included in the replacement's matched span.
This borrows the targeted, unique-match workflow from
[Pi](https://github.com/earendil-works/pi/blob/9c9ce8547e5a88cbe834a53e9a6d83a789d08e47/packages/coding-agent/src/core/tools/edit.ts)
and [Eino](https://github.com/cloudwego/eino-ext/blob/eee37f63ff9548c5eaec48fc157863339f8eda07/adk/backend/local/local.go),
without importing their runtimes or copying their write/normalization backends.

The result has `preview_only:true`, the complete candidate `content`,
`original_sha256`, `candidate_sha256` and `match_line`. Hashes cover the exact
source bytes read and exact returned candidate bytes, including BOM/newlines.
`match_line` is 1 plus the number of LF bytes before the match's first byte;
a bare CR does not increment it. Candidate length is checked before allocation,
then encoded JSON including escapes/hashes is checked against `MaxResultBytes`
(default 128 KiB, range 512 bytes to 8 MiB). Oversized/nontext/invalid input fails
without a partial candidate; runtime and child-call budgets also apply.

Fingerprints identify **this call's bytes**, not an atomic filesystem snapshot,
current on-disk contents, a lock, a durable revision or a CAS token. A concurrent
writer may change the source during/after reading. Any later apply operation
needs fresh authorization and a separate sound concurrency policy; a matching
hash check followed by ordinary rename would not provide that policy.

The preview inherits `read`'s Linux/procfs, regular-file, final-symlink and trusted
workspace boundaries. It reuses the existing bounded text reader and adds no
filesystem opener, diff framework, dependency or process. Permissions and
cancellation are checked between bounded stages and before publication; a denied
or canceled preview returns no candidate. The result contains **full file
content**, potentially including secrets: authorize it like `read`, not like a
metadata-only comparison. It does not redact or make content safe to share.

## Codemode

Pass the **same** definitions through the existing native adapter:

```go
binding := codemodetool.Native(tools[0], "workspace")
// Or explicitly bind readTool/findTool/grepTool/createTool/previewTool/replaceTool/editTool using the same adapter.
codeTool, err := codemodetool.New(sandbox,
    []codemodetool.Binding{binding}, codemodetool.Options{})
```

The alias is `binding.ExportedName()`. This preserves Agent child-call validation,
gates, permissions, cancellation and result handling; there is no direct
filesystem bridge in the JavaScript runtime. Only explicitly bound leaves are
visible. Include `codeTool` in the application definition only if wanted.
Do not expose an unrestricted shell/Python bridge and describe this root as a
security boundary for that bridge.

## Explicit ordinary atomic replacement

`AtomicReplaceDefinition(root, ReplaceOptions{MaxBytes: 64 << 10})` exposes
`atomic_replace({"path":"file.txt","content":"complete new text"})` separately
from `create_file`. Its default/maximum input limits are 64 KiB/1 MiB, UTF-8
without NUL, including empty content. Parent directories must exist. Nil permission
hooks authorize this capability when the host explicitly selects it. The model
cannot choose permission modes or upgrade `create_file` into overwrite authority.

This is **ordinary workspace overwrite semantics, not CAS**: last writer wins.
Other tools, editors or processes may change the target between observation and
publication; their updates can be overwritten. No expected-hash claim, transaction,
automatic retry, backup or rollback is provided. The tool rejects names observed
as symlinks, directories or special files, but a later name change may be replaced.
The final rename replaces a directory entry; it does not follow a final symlink.
A concurrent target deletion can result in creating the name instead.

On Linux with the already-required trusted procfs and O_TMPFILE filesystem
support, the tool writes a complete anonymous inode, synchronizes it, links a
cryptographically random temporary name in the fixed parent directory and renames
that entry onto the target. The result is `{published:true,durability:"synced"}`
on normal completion. Rename is the commit point. Directory-sync, cancellation,
revocation or cleanup failures afterwards preserve a committed host receipt; uncertain
publication errors retain `unknown`. `synced` means the requested file/directory
fsync calls succeeded, not a hardware guarantee or a promise that nobody changed
the destination afterwards. A failure before commit does not intentionally replace
the old destination. Nothing unlinks the destination as rollback.

If permission is revoked after linking the staging name but before attempting
the rename, the tool reports `replace_not_published` (or `edit_not_published`
for `edit`). A rename error with an uncertain outcome reports `*_publication_unknown`.

The new inode has **at most 0600 permissions**, subject to umask. It does not inherit
old ownership, ACLs, xattrs, timestamps or executable bits. Other hard links and
already-open descriptors continue referencing the old inode. This is deliberately
not equivalent to in-place truncation; applications needing exact metadata
preservation should not select this increment.

**Additional host requirement:** the destination directory must be controlled
against malicious same-permission writers. A temporary name exists briefly before
rename and may remain after process termination or cleanup failure. Random names
and O_EXCL-style collision handling are not protection against malicious staging
replacement. Cleanup removes only the temporary name we successfully linked and
never removes a name consumed by successful rename. The parent is a pinned inode,
not continuously checked pathname ancestry. Hostile mounts/procfs or moving a
parent outside its authorized scope remain outside the supported contract.

Permission and context are rechecked immediately before rename, after staging
publication, as well as throughout bounded writes and after commit. The check and
syscall are not atomic with external revocation; blocking syscalls are not forcibly
canceled. Linux-only support remains explicit; other platforms fail closed.

A host wanting fewer lost updates can serialize **all** its workspace mutation
entries across read/compute/publish using one shared coordinator. This tool adds
no global locking framework. Other processes, editors and bypassing callers remain
uncoordinated; ordinary advisory OS locks do not create a universal CAS contract.

## Explicit exact edit with publication

`EditDefinition(root, EditOptions{MaxBytes: 64 << 10})` exposes
`edit({"path":"file.txt","old_text":"old","new_text":"new"})`. Selecting it
is a combined content-read and overwrite capability, even if the host does not
separately expose `read`, `preview_edit` or `atomic_replace`. It does not enable
any of those other definitions. Permission checks run under the `edit` invocation
through the ordinary Agent/Codemode lifecycle; a nil hook grants the selected
capability within the configured scope.

The source must initially exist and pass the same bounded regular-text read as
`read`. `old_text` must be nonempty and occur exactly once, including overlapping
occurrences; `new_text` is required, may be empty, and must differ. Matching is
exact raw UTF-8 bytes with no fuzzy match or BOM/newline/whitespace normalization.
Unmatched bytes are retained. Each source, old/new string and full candidate is
bounded by MaxBytes (default 64 KiB, maximum 1 MiB). Not-found, ambiguous, invalid
text or oversized candidates fail before preparing a publication. The tool returns
only `{published:true,durability:"synced"}` on normal success, not the original or
candidate file content, a diff, or a version/CAS token.

The unique match applies to the **bytes read**, not to the inode or bytes present
at commit. Read and publication resolve the root-relative path separately. A
concurrent editor can replace that name or change its parent between these steps;
the candidate from the earlier file can replace the later file. If the source is
deleted after reading, publication may recreate it. This is intentional ordinary
**last-writer-wins**, not protection against lost updates, a transaction, or an
application of a previous preview. The current implementation adds no per-path
lock or mutation queue. Host-wide cooperative serialization can be applied as
described above, but is not isolation from unrelated writers.

All `atomic_replace` metadata, trusted-directory, pinned-parent, platform and
failure boundaries above apply: a new inode with at most 0600 permissions (no
old executable bit/ACL/xattr/ownership preservation), other hard links unchanged,
Linux with supported O_TMPFILE/procfs, no malicious staging-name writers, and no
rollback of the destination. Edit authorization is revalidated before staging and immediately before rename;
previous authorization to read alone does not grant this capability. Revocation/cancellation after commit still reports committed facts.
File/directory fsync successes do not guarantee storage hardware or prevent later
external updates. In-flight syscalls cannot be forcibly canceled.

## Small-PR delivery plan

1. **First increment:** optional Linux `ls`, root-relative paths, bounded output,
   cancellation/permission checks, direct and Codemode tests. Default core stays
   tool-free.
2. **Second increment:** explicitly selected Linux text `read`, pinned-inode
   reopening, byte/UTF-8/JSON limits and deterministic replacement-race tests,
   under the trusted workspace/procfs boundary above. Final symlinks remain
   unsupported. No broader read-only preset is introduced yet.
3. **Third increment:** explicit filename `find`, bounded descriptor-relative
   traversal and no child-symlink descent.
4. **Fourth increment:** explicit single-file `grep` sharing bounded text reads,
   with standard Go regexp, byte/line/match/JSON limits and no external process.
5. **Fifth increment:** explicit Linux create-only text publication, without
   overwriting any name and with commit/unknown facts retained on failure.
6. **Sixth increment:** read-only exact replacement preview with complete bounded
   candidate text and informational byte fingerprints. No apply/overwrite.
7. **Seventh increment:** explicit ordinary `atomic_replace`, no CAS, no implicit
   activation, and committed/unknown outcome facts.
8. **Eighth increment:** explicit exact `edit` reusing preview matching and ordinary
   replacement publication, without CAS or implicit write authority.
9. **Ninth increment:** explicit fixed read-only and read-and-write workspace
   presets, with ordinary Codemode assembly examples and permission regression tests.
10. **Later:** optional cooperative workspace coordination, if an application needs it.
11. **Separate optional capability:** command execution/Python with real documented
   OS isolation, resource/lifetime control and host permissions. Never infer this
   authority from a filesystem root or Codemode allowlist.

Each increment needs its own tests and independent review. New OS support must
fail closed until its directory/file-open behavior is verified, including FIFO,
reparse point/symlink and replacement-race cases. No downstream business policy
or JZ default configuration belongs in this package.
