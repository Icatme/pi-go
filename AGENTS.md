# AGENTS.md

This repository is a Go reimplementation of the `pi.ai` core package surface, currently ported from `V:\gitdownload\pi-mono\packages\ai`.

## Goal

Build a clean Go version of the `pi.ai` core semantics first, then expand only after the behavior is nailed down by tests.

## Working Rules

1. Find the root cause before fixing a bug.
   - Do not patch symptoms only.
   - Read the relevant code path and tests first.
   - Add or update tests to prove the actual failure mode when possible.

2. Avoid compatibility baggage unless explicitly requested.
   - Prefer replacing incorrect behavior directly instead of layering shims.
   - Do not keep old branches, aliases, or transitional wrappers unless the task explicitly requires backward compatibility.
   - If a breaking cleanup is the correct fix, call it out plainly and implement the cleaner path.

3. Use PowerShell-oriented workflow in this repo.
   - Assume the shell is PowerShell.
   - Do not introduce `.bat`-based edit or patch flow.
   - When scripting repository tasks, prefer PowerShell scripts/commands.

## Code Change Expectations

- Keep the implementation surface small and explicit.
- Follow existing repository style unless there is a clear reason to improve it.
- Prefer focused changes over speculative refactors.
- Do not broaden provider-specific behavior until the provider-agnostic semantics are stable.
- If behavior is unclear, derive expected behavior from tests before adding new abstractions.

## Agent Runtime Boundary

- `pkg/pigo` owns model metadata, provider protocols, authentication, and model streaming.
- `agent` owns the single-agent loop, runtime state, message lifecycle, and tool execution lifecycle.
- `agent` may depend on `pkg/pigo`; `pkg/pigo` must not depend on `agent`.
- Keep graph and multi-agent orchestration out of the `agent` core package.

## Testing

- Run the smallest relevant test set first, then broader tests as needed.
- When fixing a defect, add or update a regression test when the codebase allows it.
- If a failure cannot be reproduced locally, state that clearly instead of guessing.

## Communication

- Be direct about assumptions, tradeoffs, and breakage risks.
- If a requested approach conflicts with code health, explain the conflict briefly and choose the technically sound path unless the user explicitly wants otherwise.

## Tracked Upstreams and Provider Plugins

Maintain this explicit watchlist when working on upstream synchronization. Tracking
is not installation, native Go support, or permission to use an account or service.

| Project | Follow-up scope and evidence |
| --- | --- |
| [Pi / pi-mono](https://github.com/earendil-works/pi) | Core semantic upstream, not a plugin. Compare only behavior relevant to existing Go packages; keep implementation status in [Pi follow-up](docs/pi-1-followup.md). |
| [patlux/pi-commandcode-provider](https://github.com/patlux/pi-commandcode-provider) | Existing Command Code reference: model discovery, protocol, auth, capability and quota changes. The catalog currently cites v0.7.10 / [63c8e5c](https://github.com/patlux/pi-commandcode-provider/commit/63c8e5ccea73850266c34e655d3a76039316d4aa); see [catalog source](pkg/pigo/provider_commandcode_catalog.go) and [v0.17.2 notes](docs/releases/v0.17.2.md). This is a checked component baseline, not a claim of complete plugin parity. |
| [mikeyobrien/pi-provider-kiro](https://github.com/mikeyobrien/pi-provider-kiro) | User-selected Kiro plugin to follow. Initial review baseline: v0.12.3 / [dd93f2e](https://github.com/mikeyobrien/pi-provider-kiro/commit/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba). In the development stack, scoped buffered and incremental text/tool adapters now have synthetic Go/race coverage; this is neither full plugin parity nor live validation. See [Kiro follow-up status](docs/kiro-followup-status.md). |

- Do not substitute similarly named forks without recording why and verifying the
  exact repository. Add other plugins only with a user decision or repository
  evidence; do not infer a broader agreed list from this initial watchlist.
- For each follow-up, record the inspected tag/commit, relevant delta, Go impact,
  tests actually run, and unresolved limitations in [the shared worklog](docs/PO_AGENT_WORKLOG.MD).
  Separate research, implementation, pushed/merged code, and live verification.
- Review upstream licensing, service terms, supported interfaces and account
  entitlement before adapting provider behavior. Community code is not proof of
  service authorization; do not bypass plan restrictions or guess private protocols.
- Keep authentication caller-owned and explicit. Do not silently import CLI
  tokens, copy client identities, install plugins, start login flows, or make
  billable live requests as part of synchronization. Prefer offline fixtures;
  real credentials, service calls and additional billing require explicit scope.
- Keep Kiro ACP exploration separate from this plugin follow-up. An ACP harness
  is not a drop-in model provider; denying permission callbacks is not a sandbox.
