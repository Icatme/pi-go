# Optional filesystem tools: first increment

`fstools.Definitions(root, options)` creates one ordinary Agent definition:
`ls`. Nothing is installed or enabled in the core Agent, CLI, managed MCP
configuration, or a downstream application. This is not a complete filesystem
suite: `read`, `find`, `grep`, `write`, `edit`, shell and Python are not supplied.

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
hook means the host permits all directory listings under that root.

## Contract and limits

`ls({"path":"."})` lists the root. A path is required, valid Unicode, at most
4096 UTF-8 bytes, relative and slash-separated. Absolute paths, any `..`
component, NUL, backslashes and colons fail. Relative symlinks are allowed only
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

## Codemode

Pass the **same** definitions through the existing native adapter:

```go
binding := codemodetool.Native(tools[0], "workspace")
codeTool, err := codemodetool.New(sandbox,
    []codemodetool.Binding{binding}, codemodetool.Options{})
```

The alias is `binding.ExportedName()`. This preserves Agent child-call validation,
gates, permissions, cancellation and result handling; there is no direct
filesystem bridge in the JavaScript runtime. Only explicitly bound leaves are
visible. Include `codeTool` in the application definition only if wanted.
Do not expose an unrestricted shell/Python bridge and describe this root as a
security boundary for that bridge.

## Small-PR delivery plan

1. **This PR:** optional Linux `ls`, root-relative paths, bounded output,
   cancellation/permission checks, direct and Codemode tests. Default core stays
   tool-free.
2. **Next:** bounded text `read`, after resolving file-type/open races and special
   file handling per supported OS. Prove regular-file reads, byte/UTF-8 limits,
   symlink replacement behavior and cancellation limits before adding a preset.
3. **Then:** `find` and `grep`, with independent visited-entry, depth, scanned-byte,
   per-file/line, match and output budgets. No full-tree buffering before truncation.
4. **Later:** explicit workspace `write`/`edit` capabilities with host approval,
   atomicity and conflict semantics; read-only/workspace presets made only from
   implemented definitions.
5. **Separate optional capability:** command execution/Python with real documented
   OS isolation, resource/lifetime control and host permissions. Never infer this
   authority from a filesystem root or Codemode allowlist.

Each increment needs its own tests and independent review. New OS support must
fail closed until its directory/file-open behavior is verified, including FIFO,
reparse point/symlink and replacement-race cases. No downstream business policy
or JZ default configuration belongs in this package.
