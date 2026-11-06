# M1d: structured references to multiple public reports

2026-11-06. Follow-up to the M1c gap on #188, remote parent
`13ff33b3204c339054a4fa44a8bf5d011cf34fdb` (tree
`aa1f8178fb8374b4f9b0e2a83a21a9ec332828e1`). M3b/M3c already expose bounded
public history, but no prior slice lets a decision select several report facts
for one durable reply. This slice adds that contract, not a new scheduler.

The slice was initially tested on #188 at `c55c41d`. Before publication, the
branch advanced with a separate integration/review. All 21 changed paths were
materialized and the complete new base tree verified before transplanting this
feature. The worklog keeps both authors' entries; the newly added failed-input
hidden-action guard is retained using the slice-aware zero-decision check.
All checks and independent review are repeated on the new combined tree. Earlier
results are not reused as verification of this parent. The other worklog's
Windows/WSL/PostgreSQL findings are preserved history, not runs by this slice.

## Contract

A `MainDecision` with action `reply` or `clarify` can set `ReportRefs` to 1–16
unique `HistoryRef` values. Each reference must have the exact main-session
`Domain`, `Kind: "report"`, and canonical report `ID`. Copy references from
`MainInput.HistoryRefs` or follow existing `MainHistory` links. A report identity
is its domain and delivery ID, not source/event alone: two fanout recipients
have distinct reports even when the external event is the same.

The coordinator checks the references against the immutable public history
view captured for this invocation. A report that finishes while the callback is
running is unavailable until a later callback. Missing, foreign-domain,
non-report, duplicate, hidden, unreported, noncanonical and oversized selections
fail the input; an invalid member of a batch fails the whole group before any
reply or assignment is applied. An explicit empty list is invalid; omit the field
when no citation is wanted. The strict Pi parser also rejects null, unknown
fields, duplicate JSON keys, case aliases and model-authored report contents.

After validation, the coordinator resolves each reference from durable report
state and creates `Reply.Reports []ReportCitation`, in requested order. Each
citation contains `Ref` and the original public `Report`, including exact
source/thread/event/task/delivery, terminal status, summary, native receipts,
background facts and `NeedsReview`. Successful, truncated, tool-error, failed,
interrupted and blocked outcomes are not collapsed into a synthetic success.
No model field can replace these facts. A committed effect remains committed
when the run fails; an absent receipt remains unknown, not proof of rollback.

`Reply.Report` remains the triggering result for existing single-report
consumers. It is not replaced with the first citation. It remains present even
when the model cites different reports, and nil for an ordinary user-summary
reply. `TaskID`, `Control`, `Handback`, destination and delivery status keep their
existing meanings. A cited report needing review does not itself mark a reply's
*transport* as uncertain. Hosts using the new feature should inspect both the
triggering report and the explicit citations. Selecting the triggering report
also includes it in `Reports`; duplicate explicit references are rejected.

All four Pi main adapters accept `report_refs`: single, batch, history, and
batch-history. No new tool, execution grant or destination selector is added.
See the runnable external-package `ExampleMainDecision_reportRefs`.

## Privacy and authority

References confer no new authority. The public boundary remains the journal's
single host authorization domain and immutable ingress decision. Rejected
admissions and noise, including inherited fanout slots, cannot be cited. Legacy
or privileged `Submit` records have no admission gate; their non-noise public
reports remain visible as before. Hosts must use separate journals for distinct
trust domains and apply their existing outbound authorization policy.

Resolution never reads worker/private main snapshots, private failure causes,
raw tool arguments or private background output. Returned citations and nested
receipt slices are detached. Already public text is host-owned evidence, not
automatically redacted or newly safe for another audience. This contract is not
an access-control policy change or a claim that model summaries are truthful.

The list proves that attached facts identify existing public reports. It does
not prove the model actually read the report, fact-check arbitrary natural
language, map individual sentences to facts, verify external effects, or add
source URLs. Channel adapters still send only approved `Reply.Text`; they do not
automatically append these structured facts or perform semantic validation.
The host remains responsible for rendering/approving appropriate outgoing text.

## Persistence and compatibility

Only the selected identities are added to the existing decision record. Reports
remain authoritative in their original terminal transitions; replay resolves
those immutable records and reconstructs the same citations and reply identity.
Replay validates report existence, canonical identity, domain and ingress
visibility at that journal position. Callback-time visibility is a live check;
its exact historical freeze is not separately persisted or cryptographically
attested. The journal remains trusted host storage, not tamper-proof evidence.

No new journal state or dependency is introduced. Older journals without the
field retain their original replies with nil `Reports`. The existing claim,
uncertain-send, reconciliation and duplicate-event protocol is unchanged.
Citation decisions before/after append recover without rerunning terminal work
or duplicating replies. Reopen cannot safely retry an uncertain external send.

Existing consumers that use `Reply.Report` keep their behavior. Consumers that
ignore the new field will not see the extra citations. Adding a slice makes
`MainDecision` no longer Go-comparable; callers that compare whole decisions or
use them as map keys must compare fields/use an explicit key. This is an additive
JSON contract, not a guarantee that old binaries preserve new citation metadata
when reading newer logs. Upgrade readers before writing citation decisions;
do not downgrade an active journal to discard those facts.

## Verification and scope

Test-first compilation failed because `ReportRefs` and `Reports` did not exist.
Focused tests cover:

- Mixed success/tool-error/committed failure facts in single and batch modes;
  primary single-report compatibility, detached data, private canaries,
  completed reopen, duplicate admission and uncertain delivery fencing.
- Frozen callback visibility while an independent task finishes, public ingress
  exclusions, two-recipient report identities, nested background copying,
  invalid-group atomicity and all four real Pi adapters with offline scripts.
- Citation decision append boundaries and forged replay rejection for foreign,
  absent, wrong-kind, duplicate, wrong-action and empty selections.
- Strict JSON parsing and the external Go example.

Final full repository tests/race/vet, separate examples-module checks and
independent review are recorded on the draft PR's exact tree. Tests use the
existing remote Linux/amd64 host and cached Go 1.26.10. PowerShell is unavailable
there; Bash equivalents are used. Reproduction with PowerShell on another
already authorized environment:

```powershell
$env:GOPROXY = 'off'
$env:GOSUMDB = 'off'
$env:GOTOOLCHAIN = 'local'
$env:GOWORK = 'off'
$env:PIGO_LIVE_TEST = '0'
$env:PI_GO_AGENT_LIVE_TEST = '0'
$env:PI_GO_AGENT_EXAMPLE_LIVE = '0'
$env:PIGO_POSTGRES_TEST_DSN = ''
$env:PIGO_POSTGRES_TEST_DISPOSABLE = ''
go test -mod=readonly -count=10 -run '^(TestReportReferences|ExampleMainDecision_reportRefs)' ./superagent
go test -mod=readonly -race -count=5 -run '^(TestReportReferences|ExampleMainDecision_reportRefs)' ./superagent
go test -mod=readonly -count=1 ./...
go test -mod=readonly -race -count=1 -timeout=15m ./...
go vet -mod=readonly ./...
Push-Location examples
go test -mod=readonly -count=1 ./...
go test -mod=readonly -race -count=1 ./...
go vet -mod=readonly ./...
Pop-Location
```

No real model, external send, live database/device/platform, user computer,
Codex/extra paid execution, manual CI, merge or deployment. PostgreSQL integration
remains intentionally gated/skipped. No Windows/macOS, production permission,
semantic model quality or external exactly-once guarantee is claimed. The
remaining M1–M9 production/release gates stay open.
