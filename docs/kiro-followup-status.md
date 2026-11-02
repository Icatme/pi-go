# Kiro plugin follow-up status — 2026-11-02 UTC

This development integration includes the plugin watchlist from draft [#50](https://github.com/Icatme/pi-go/pull/50),
plus its updated implementation status. It does not merge or deploy master.

## Upstream check

Read-only GitHub check at 05:09 UTC: upstream `main` is still
[dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba](https://github.com/mikeyobrien/pi-provider-kiro/commit/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba),
commit dated 2026-10-29 22:21:36 UTC, message `chore(release): v0.12.3`.
The pinned [package.json](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/package.json)
says version 0.12.3 and license MIT. No new main delta was observed relative to the
implemented baseline. Tag-list endpoint was unavailable through the connector;
this is not a claim that every tag/release/branch was checked. No plugin install,
service request or account availability was tested.

## Actual development status

| Area | Implemented evidence / remaining boundary |
| --- | --- |
| Request/auth/client headers | Explicit scoped source, bounded serialized request, caller-supplied origin/user agent and injected HTTP. No implicit CLI token discovery, login, refresh, default network or real client identity guarantee. |
| Buffered Agent tools | Explicit registration, session affinity/Auto opt-in, in-memory exact raw-argument evidence, fake multi-turn permission/cancellation tests. Persisted tool-history restore remains unsupported. |
| Live text | Separate explicit registration plus Auto/session options: [#107](https://github.com/Icatme/pi-go/pull/107), [#108](https://github.com/Icatme/pi-go/pull/108), [#109](https://github.com/Icatme/pi-go/pull/109). Actual Agent delta precedes synthetic terminal bytes; not buffered replay. |
| Mixed text/tools | Pure validated terminal batch [#110](https://github.com/Icatme/pi-go/pull/110) and internal injected worker [#111](https://github.com/Icatme/pi-go/pull/111). Text provisional; tools held until HTTP EOF/length/Close and terminal validate. A subsequent explicit mixed registration now has actual Agent fake permission/late-cancellation coverage; see [incremental registration](kiro-explicit-incremental-registration.md). |
| Diagnostics | Context percentage retained as bounded diagnostic/Agent metadata, not tokens, credits or billable usage. |
| Upstream tests | Independently authored scenario/fixture migration with Go tests; not copied test corpus or full upstream parity. See protocol/history/request migration documents and feature docs. |

All listed PRs were pushed as drafts, not merged to master, released or deployed
at this snapshot. Fake-service tests do not establish real endpoint acceptance,
subscription entitlement, OAuth compatibility, actual billing or production readiness.
Reasoning/signature replay, richer image/history semantics, metering, default login
and implicit service calls remain unsupported or explicitly deferred.

## Baseline verification record (before the subsequent increment)

Code tree from #111: `06de94c8abe4578ff5e1cb631c09e217a6b2a50c`.
Official Go 1.26.2, cached dependencies, live test flags disabled:
focused mixed/live-worker race repeated three times passed (2.120s); final-tree
full repository race passed (pigo 31.864s), plus vet/gofmt/diff check.
Independent parent review was static, not a separate test execution. No CI was
dispatched. This watchlist integration changes documentation only; code blobs
are retained from that tested tree and validated separately by exact comparison.


## Subsequent explicit mixed registration increment

The separate RegisterKiroIncrementalModule API now selects the internal mixed
worker without changing buffered or text-only entry points. Explicit Auto/session
options remain required. Actual fake Agent tests cover successful and permission-
denied two-turn tool results, ToolCallEnd-triggered cancellation with zero execution,
and truncated/Close-failed responses with no tool authorization. This is still
unmerged development code and synthetic verification, not real service validation.
See the worklog for this increment's actual final test results.

## Subsequent offline refresh-response contract

[Offline refresh response](kiro-offline-refresh-response.md) adds strict supplied-
JSON semantics for explicit IDC/desktop/external-IDP families, scope, expiry and
refresh-token rotation. It does not discover credentials, send a refresh request,
install a credential source or persist access. The worklog records its actual Go
and race runs; automatic login/refresh remains unsupported.

## Subsequent scoped catalog selection

[Scoped catalog selection](kiro-scoped-catalog-selection.md) now binds offline
observations to explicit scope/time and requires alias/service-ID namespace plus
freshness policy. It does not fetch a catalog, select a default model, assert
account access or connect service-ID routing to the registered provider yet.

## Subsequent management request assembly

[Offline catalog request](kiro-offline-catalog-request.md) constructs a scoped
ListAvailableModels GET with explicit auth/client inputs. It never sends HTTP or
updates a registry, and does not add upstream's profile probes or region fallback.

## Subsequent injected management response pipeline

[Injected catalog attempt](kiro-injected-catalog-attempt.md) now connects an explicit
fake-testable executor to bounded JSON/status/Close checks and a scoped observation.
No default client, automatic fetch/cache or provider/model registration is added.

## Subsequent explicit public catalog API

[Public catalog observations](kiro-public-catalog-observation.md) expose a required-
configuration fetch and immutable scoped Models/Select observations. External Go
and race tests cover callback failures, detached concurrent access and unchanged
registration. This does not register models, prove entitlement or connect model
selection to runtime routing; real service validation remains outstanding.
