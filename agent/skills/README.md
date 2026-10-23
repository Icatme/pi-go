# Trusted skill catalogs

`agent/skills` is a small, opt-in filesystem layer inspired by Pi's progressive
skill loading. A skill is **instructions and supporting files**, not an executable
plugin, a new permission, or a separate agent runtime. Nothing changes in the
agent loop or any existing tool unless the embedding application opts in.

## Use

```go
catalog, err := skills.New(skills.Options{
    Roots: []string{"/srv/my-app/approved-skills"},
})
if err != nil { return err }
defer catalog.Close()

// Add only routing metadata to the model's context.
systemPrompt += "\n" + catalog.Prompt()

// In an application-provided, permission-checked read_skill tool:
body, err := catalog.Read("spreadsheet")
// Read a supporting UTF-8 document only when requested:
reference, err := catalog.ReadResource("spreadsheet", "references/formats.md")
// Administrative reload; a fatal error preserves the previous catalog.
err = catalog.Reload()
```

The snippets illustrate the API; applications must handle each returned error.
Expose reader tools through your existing tool permission checks, not a generic
filesystem or shell bypass. `Prompt` refers to a permitted reader, but does not
register one. Applications may adapt these methods to their tool schemas and
use `ToolResolver`/`PrepareNextTurn` to select skills for an individual turn.

## Bundle format

```text
approved-skills/
  spreadsheet/
    SKILL.md
    references/formats.md
    scripts/convert.py
```

```markdown
---
name: spreadsheet
description: Create and inspect spreadsheets. Use for Excel workbooks and CSV analysis.
disable-model-invocation: false
---
Read references/formats.md before producing a workbook.
```

YAML scalar types are checked rather than coerced. Names must be 1–64 lowercase
ASCII letters/digits with single separating hyphens. Description must be a
non-blank string of at most 1024 Unicode code points. Description block scalars
are supported. Names/descriptions cannot use aliases; duplicate top-level fields
are rejected. Unknown fields are ignored; notably `allowed-tools` does **not**
grant anything. Invalid bundles produce diagnostics and are excluded.

`disable-model-invocation: true` omits a skill from `Prompt`, not `List` or `Read`.
The host must require explicit selection before exposing such a skill to a model.
Command parsing (for example `/skill:spreadsheet`), user/tenant selection and
conversation persistence belong to the embedding application, not this package.

## Discovery and reload

- Only explicit absolute roots are searched, in supplied order. No HOME, parent
  repository, network, registry, `.agents`, or other implicit discovery occurs.
- Paths within a root are visited lexicographically. Only `SKILL.md` bundles are
  supported; loose Markdown files and ignore-file rules are not supported.
- Finding `SKILL.md` ends recursion into that directory, even if invalid. Hidden
  directories and `node_modules` are skipped. Discovery does not follow symlinks.
- First valid name wins. Duplicates, skipped symlinks, and invalid bundles are
  reported by `Diagnostics`. A missing/unreadable root or exceeded scan limit
  fails the entire load/reload rather than silently deleting existing skills.
- Defaults: 256 KiB per text file, 4096 scanned entries, 16 directory levels.
  `MaxFileBytes` can be at most 16 MiB, depth at most 128.
- Lists and diagnostics are detached copies. Catalog operations are synchronized;
  reload installs a complete index and closes superseded directory handles.
- Body text is read on demand and is not stored in the catalog or prompt. If
  frontmatter changes, `Read` requires reload. Body-only edits are visible on the
  next read. Reload is explicit: there is no watcher or periodic task.

## Trust and filesystem boundary

Configure roots only from trusted administrator configuration, never from model
output or arbitrary tenant input. The catalog pins each skill's directory handle
and uses Go `os.Root` for reads. Resource paths must be slash-separated relative
paths without dot traversal, backslashes, or colons. In-bundle relative symlinks
may be read, but symlink escapes and absolute links fail. Only bounded UTF-8 text
without NUL is accepted; directories and non-regular files are rejected.

This is **not a sandbox for malicious local writers**: provision roots and their
parents so untrusted users cannot replace them or race reads. Go `os.Root` does
not prevent crossing mounts or hard links. Do not place device files, `/proc`,
untrusted mounts, or secrets in approved roots. JavaScript and Plan 9 targets are
rejected because they do not provide the required directory-handle guarantees.
Review skills and resources before approval. XML escaping prevents malformed
catalog markup, not semantic prompt injection. Loaded text must remain subordinate
to system instructions and tool authorization, including user/tenant boundaries.

No scripts are run, no dependencies installed, no Python environment created,
and no tool permission is widened. A Python/uv document runner should be a
separate application-authorized tool with its own deployment, input/output limits,
permission checks, and artifact delivery. It can consume approved skill guidance
without moving business-specific dependencies into pi-go.

## Compatibility and provenance

This implements the portable SKILL.md and progressive-loading concepts, not the
entire Pi coding-agent package manager. Unlike Pi's permissive warnings, invalid
names/types are rejected, symlink discovery is disabled, and roots are explicit.
No existing pi-go API changes. The only added dependency is `gopkg.in/yaml.v3`
v3.0.1 for standard YAML parsing rather than a partial homegrown parser.

References:
- https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/skills.md
- https://github.com/earendil-works/pi/blob/main/packages/coding-agent/src/core/skills.ts

Validation: `go test -race ./agent/skills` and `go vet ./agent/skills`.
No live-model/service tests are necessary for this filesystem-only package.

## Compile-time bundles with `embed.FS`

Applications that require skill changes to go through source review and binary
redeployment should use `NewFS`, not writable roots or the disk catalog:

```go
//go:embed builtin
var bundledSkills embed.FS

catalog, err := skills.NewFS(bundledSkills, skills.FSOptions{
    Roots: []string{"builtin"},
    Version: "document-skills-v1",
})
if err != nil { return err }
```

`FSCatalog` has `List`, `Diagnostics`, `Prompt`, `Read`, and `ReadResource` with
matching signatures, plus `Version`. It has no `Reload` or `Close`. Construction
copies the approved UTF-8 text and metadata; no source filesystem is retained or
accessed on subsequent calls. Mutating a `fstest.MapFS` after construction cannot
change the catalog. A changed embedded skill requires rebuilding and deploying
the application; command routing and permission checks still belong to the host.

The `Version` option is the application's stable build/content label. It is not a
cryptographic digest or a permission token; the application must update it when
appropriate. No content hashes are recomputed during reads. Prompts remain
metadata-only even though text is held in memory, preserving progressive loading
into the model's context. Automatic invocation exclusions are identical to the
disk catalog and must still be enforced by the embedding host.

FS roots use slash-separated `fs.ValidPath` syntax, defaulting to `.`; absolute
paths, backslashes, colons and traversal are rejected. Locations are relative FS
paths, not host filesystem paths. Bundle resource trees are copied, including
nested files, but are not discovered as separate skills. Only regular UTF-8 text
without NUL is supported: binary templates/assets must use a separate artifact
mechanism. No scripts are executed even when their source text is bundled.

Defaults: 256 KiB per file, 16 MiB total text, 4096 scanned entries, and 16 levels
across discovery and resources. Total text has a hard configurable maximum of
256 MiB. Invalid metadata and duplicate names produce diagnostics; invalid text,
unreadable files, resource symlinks/special files, or exceeded limits fail
construction. Review diagnostics at startup to detect a missing intended skill.
Unlike the disk catalog, resource text is validated during construction, not on
its first read. There is no mutable metadata/read mismatch in an FS snapshot.

The supplied `fs.FS` implementation is trusted code. Path validation is not a
sandbox for an arbitrary implementation, `os.DirFS`, or a backend that hides
symlinks or performs network requests. Prefer `go:embed` for compile-time-only
policy. Symlinks identified during discovery are skipped with diagnostics;
symlinks identified inside an accepted bundle are rejected. Do not pass user-
controlled external backends to implement an embedded-only policy.
