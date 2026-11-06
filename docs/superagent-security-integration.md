# SuperAgent editor and history security integration

This development slice composes two independently reviewed fixes without
changing either production boundary beyond its original patch:

- Base: draft [#177](https://github.com/Icatme/pi-go/pull/177),
  `fix/superagent-history-admission`, exact commit
  `2b16d08e209f9e615452a4c2a01b108c324c64ed`, tree
  `912d3b8ba41bf011d8b98f31eeb0665866af236e`.
- Transplant: draft [#176](https://github.com/Icatme/pi-go/pull/176),
  `fix/superagent-github-edited-actor`, exact commit
  `a044bdd1a01ba6edc50312fbf42b71c016b3a0c2`, tree
  `b9fd4eb5c17a6ab5635de7cb8d85ee55053451b2`.
- Both fixes originally depend on draft #175 at
  `cc3bb8efc8a10f1e46671d349947e3342ee9dec6`.

The five source code/test/documentation files from #176 are preserved verbatim.
All history implementation, regression, upgrade and audit files from #177 remain
unchanged. The historical worklog is retained byte-for-byte from #177; this
separate note records composition rather than rewriting either earlier record.
The new code in this slice is a composition test, not another production change.

## Combined boundary

An edited GitHub issue or review comment uses the authenticated sender as its
policy principal. A distinct original author is valid only for these supported
edited actions and must still have a canonical positive uint64 ID. Creation and
review submission retain the author/sender equality check. The original author
cannot grant the editor access; organization, PR target, object identity, intent
and task/thread associations continue to come from authenticated host facts.

Rejected edits and noise remain in trusted-host audit data but cannot become
model-visible history through a later authorized editor. Original frozen
admission decisions determine visibility after policy changes, deduplication and
reopen. Public history references do not grant task or control authority.

The [history admission upgrade warning](superagent-history-admission.md#upgrade-and-existing-exposure)
still applies. Filtering the catalog does not erase rejected text already saved
by an older callback in model snapshots, summaries, replies or effects. Restart
does not sanitize an exposed session. Preserve the journal for trusted-host
review and choose a safe recovery plan before resuming a potentially contaminated
session. This integration performs no destructive migration or automatic cleanup.

## Composition regression

`TestGitHubEditedCommentHistoryAdmissionComposition` exercises the real channel
adapter and public history API for both supported comment events and both single
and batch routers. It verifies:

- Editor 8 is policy-denied even when the original author is authorized user 7.
- Later authorized editor 7 may edit author 8's comment and read public history.
- Denied edit and noise text are absent from direct guessed references and
  paginated trigger/thread/report graph traversal.
- Reopen retains the prior authorized, model-ignored edit in public history.
- Replaying a denied edit under a policy that now permits its editor preserves
  the original denial and history exclusion.
- Private journal text, admission records and report-count audit are retained;
  payload task claims, history reads and replay create no tasks, replies or
  transport calls.

The new regression fails on either single-fix tree and passes on their composed
tree. Existing native Pi tool/snapshot, cursor, fanout, task/control, malformed
author, spoofed identity and own-message echo regressions remain intact.

## Verification scope

Validation uses remote Linux amd64, Go 1.26.10 and cached modules with
`GOPROXY=off`, `GOTOOLCHAIN=local` and live-test flags disabled. It covers full
readonly tests/vet, serialized full race (`-race -p=1 -count=1 ./...`), focused
history/GitHub race repeated 20 times, and the separate examples module's tests
and vet. Independent read-only review found no actionable security issue and
separately passed the focused suites and composition regression with race
enabled, repeated three times.

Exact final remote commit/tree and check status are recorded in the integration
PR. No live model/provider/GitHub/device operation, credentials, user computer,
Codex or extra paid execution, manual CI, merge or deployment is part of this
validation. PowerShell is unavailable on this remote host; no Windows/macOS or
PowerShell execution is claimed. A draft PR is not a merged or released fix.
