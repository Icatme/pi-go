# Public history admission boundary

Security correction stacked on draft #175, exact remote base
`cc3bb8efc8a10f1e46671d349947e3342ee9dec6` (tree
`bcef2e95d39bd931101932ab3be1bfea3dcf5de6`). This is a development change, not a
merged release or a claim that an existing deployment has been remediated.

## Reproduced failure

M3a correctly prevented rejected ingress from invoking the Router or Worker.
M3b's public catalog nevertheless indexed every original journal admission,
including ignored input. A later authorized input on the same source/thread
received a thread reference whose trigger links exposed rejected text both as
link summaries and complete trigger JSON. Directly guessed delivery references
also exposed ignored reports and their backlinks. The failure reproduced for
policy-denied requests and noise on #175; tests additionally covered unassociated
signals, requests without explicit intent, and privileged/legacy noise.

The tests were run before the fix and failed on both OpenMain and OpenMainBatch.
The external public-API probe changed from `leaked=true` to `leaked=false` for
both original scenarios after the fix.

## Visibility and authority

One predicate checks the original immutable admission of every delivery:

- A gated input is visible only when its frozen decision is `accepted`.
- Noise is never visible, including privileged Submit and pre-gate logs that
  have no AdmissionRecord.
- Other legacy/privileged input retains its existing visibility. A model's
  later `ignore` decision is not an admission rejection and does not hide it.
- Fanout descendants inherit the original admission's visibility, while each
  authorized delivery retains its distinct report identity and task links.

The predicate guards trigger/thread creation, task associations, report
publication and offered HistoryRefs. Hidden inputs have no catalog node or
link, so direct reference guesses and continuations cannot retrieve them.
Threads start at the first visible admission, not a rejected audit slot.
Appending hidden records does not change public node revisions or invalidate
unrelated authentic continuations. Existing page limits, frozen views, callback
expiry, restart key rotation and domain checks are unchanged.

Admissions, Status and ReportsSince remain trusted-host audit surfaces. Their
records, original journal text, deduplication decisions and report-count cursors
are preserved. Hosts must not pass those audit records back to a model. Neither
history recall nor a recalled task ID expands task/control offers or authorizes
Worker tools. No journal format, dependency or execution behavior is changed.

## Upgrade and existing exposure

The catalog is process-local and rebuilt on reopen. Existing journals therefore
receive the new visibility rule without rewriting the audit log. Previously
offered references to hidden trigger/report nodes become unavailable. A thread
reference previously based on a hidden first admission also becomes unavailable;
use the new HistoryRefs from a fresh authorized callback. Old cursor tokens
already expire at restart. Stop the exclusive old writer before upgrading.

**This correction does not erase information already disclosed.** A successful
old Pi history callback may have persisted rejected text in native tool results
inside the main snapshot; later model replies, task summaries or effects may
also derive from that text. The new index does not sanitize or discard existing
snapshots, rewrite audit records, or undo effects. Restart alone is not proof
that such a session is clean. If the vulnerable reader was used with rejected
ingress, preserve the journal for trusted-host incident review and do not resume
model processing on a potentially contaminated session until the host has chosen
a safe recovery plan. Starting a separate clean journal is an explicit host
decision, not an automatic migration; it does not carry task or control authority
from the old journal.

## Verification

Offline regression coverage includes both router modes, native Pi history tool
calls and their saved snapshots, all ignored admission reasons, direct hidden
reference guesses, paginated graph traversal, cache extension, cross-callback
continuation, authorized task/fanout graphs, changed-policy duplicate submission,
reopen and pre-gate legacy journal upgrade. Accepted model-ignored input remains
readable; audit text and report records are retained. Private Worker history and
existing task/control authorization tests remain part of the full suite.

Verification uses remote Linux amd64 with Go 1.26.10, cached modules and live-test
flags disabled. No live provider/model/service/device, user computer, Codex or
extra paid execution, manual CI, merge or deployment is involved. PowerShell
is unavailable on this host; no Windows/macOS validation is claimed.

Final checks passed: full readonly tests and vet, full race with package
parallelism limited to one (`go test -mod=readonly -race -p=1 -count=1 ./...`),
separate examples tests/vet, focused admission-history race tests repeated 20
times, and gofmt/whitespace checks. Independent read-only security review found
no blocker and separately passed focused race tests three times.

An initial default-parallel full race run and one focused 20-repeat run hit the
unchanged `pkg/pigo` stream test's two-second producer deadline, without a race
detector report. The same stream test passed 20 times on the unchanged #175 tree;
the final serialized full race run passed on this patch. Provider code and tests
were not changed to hide that timing sensitivity.
