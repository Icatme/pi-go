# Pi issue-fix integration, 2026-11-14

This is an isolated integration of the open PR stack, not a merge into `master`, release, or deployment. Baseline: `ae4e17f9022e40b17a1030824ce9a7a075ca610b`.

## Contents

All changes from PRs #200–222 are included. Existing PR branches remain untouched. The integration snapshot can be reviewed as a whole; do not independently merge it and then reapply the same patches from the individual PRs.

| Source PR | Verified source head |
| --- | --- |
| [#200](https://github.com/Icatme/pi-go/pull/200) | `2d5f5a0abf03cc698e8415383569ad7160f88b45` |
| [#201](https://github.com/Icatme/pi-go/pull/201) | `3ca8948d2ea81504ae27e706d83c0f31e5980c95` |
| [#202](https://github.com/Icatme/pi-go/pull/202) | `0df577b5552106d0e262dd213a4c2ac157815df1` |
| [#203](https://github.com/Icatme/pi-go/pull/203) | `ffed5228acdd9aeff34de5f6c349f50d3ba8db78` |
| [#204](https://github.com/Icatme/pi-go/pull/204) | `84351ee626657ab92b10c2af3f3cfbfa09fea5e8` |
| [#205](https://github.com/Icatme/pi-go/pull/205) | `ffc7c286d290b35189414abcb814b4df85b6cd25` |
| [#206](https://github.com/Icatme/pi-go/pull/206) | `c2749ac9b5b0fdcfc14d363260c2a718dbe10f23` |
| [#207](https://github.com/Icatme/pi-go/pull/207) | `8712227c3a9fad8a6f7441036c566e1eb034c615` |
| [#208](https://github.com/Icatme/pi-go/pull/208) | `4c82a9dc7b4ee23c02ee0333741bf105585d7aa6` |
| [#209](https://github.com/Icatme/pi-go/pull/209) | `8d678d5ebf9582d769dd13da456120207ae672ec` |
| [#210](https://github.com/Icatme/pi-go/pull/210) | `cc8cd8926fd9ce8b7af3e6d697723505e49d8f74` |
| [#211](https://github.com/Icatme/pi-go/pull/211) | `13d4696776a14451732311429d49d64f267637e1` |
| [#212](https://github.com/Icatme/pi-go/pull/212) | `aca0152dc5069c28f0b87e8f84b229df4b00005f` |
| [#213](https://github.com/Icatme/pi-go/pull/213) | `235012da7391933d8942a3b94c50ccc7e24b262d` |
| [#214](https://github.com/Icatme/pi-go/pull/214) | `02e263cb780ae81a440ce2024490dd99b30aceab` |
| [#215](https://github.com/Icatme/pi-go/pull/215) | `f0b03a48ff003b1dee0c7b9a10f1144e21663211` |
| [#216](https://github.com/Icatme/pi-go/pull/216) | `f7fc146ebf5c0340f052a6bc71b02adffbbf3782` |
| [#217](https://github.com/Icatme/pi-go/pull/217) | `fe5e2c85db1554c772da9cf03222c779a169681f` |
| [#218](https://github.com/Icatme/pi-go/pull/218) | `716fce85acbc4acf67a96dd19e412183d8fdd902` |
| [#219](https://github.com/Icatme/pi-go/pull/219) | `8662ead0e25459380875864498efef49d5824ad4` |
| [#220](https://github.com/Icatme/pi-go/pull/220) | `d2b6bdfd1fcf4512835c3698be82bf1d7a561004` |
| [#221](https://github.com/Icatme/pi-go/pull/221) | `9f01166880c0a156c7b05c762ceac60720c98c5d` |
| [#222](https://github.com/Icatme/pi-go/pull/222) | `79b58522aefec748d7f9fa2e226d05ccfb499cca` |

## Conflict decisions

- Shared worklog additions were retained from both sides; no prior worklog content was deleted.
- Google (#205 + #219): retain the body-read error chain with `%w`, then add the actual non-2xx HTTP status. Context cancellation and deadline identity must survive the status wrapper.
- Anthropic (#202 + #220): retain `applyRequestError`, wrapping its error with the actual HTTP status rather than replacing the cancellation-aware call with `recordProviderFailure`.
- Added cross-regressions for explicit cancel/deadline × HTTP 200/503. They require the aborted result and `cancelled`/`timeout` classification; the covered native paths retain 503 and never invent an error status for 200. Hosted Anthropic cancellation remains covered without claiming the hosted path has gained HTTP-status parity.
- Independent read-only review of the two conflict decisions found no blocking issue; its cross-regression suggestion is included.

## Verification

Go 1.26.11 on remote Linux, offline fixtures only:

- `pkg/pigo` and `agent/session` race suites passed (34.808s and 14.214s).
- Final added cancellation/status cross-regressions passed with race (1.345s). Isolated Go overlays that discard the Google status wrapper or replace Anthropic `applyRequestError` with `recordProviderFailure` each make the respective cancel/timeout × 503 regressions fail, verifying that the tests detect either incorrect conflict resolution. Overlays did not modify the tested integration source.
- Examples module tests and vet passed.
- `go mod verify`, read-only module graph, and diff whitespace checks passed.
- Full `go test -p 2 -race ./... -count=1` passed across all 29 test-bearing packages (including runtimeeval 403.169s, provider 33.471s, SuperAgent 21.344s); `go vet ./...` passed.

No live provider calls, paid model usage, Windows/macOS execution, manual GitHub CI dispatch, release, merge into master, downstream dependency update, or deployment. The repository workflow is dispatch-only. Native fallback continuation, durable per-tool crash reconciliation, auxiliary billing attribution and broader live-provider compatibility are not completed by this integration.
