# AGENTS.md

This repository is a Go reimplementation of the `pi.ai` core package surface, currently ported from `V:\gitdownload\pi-mono\packages\ai`.

## Goal

Build a clean Go version of the `pi.ai` core semantics first, then expand only after the behavior is nailed down by tests.

Provider scope for the current stage:

- implement only `OpenAI OAuth`
- implement only `Kimi Coding`
- do not add other providers unless explicitly requested
- do not port local AI / local inference provider support unless explicitly requested
- do not port generic OpenAI-compatible local server support unless explicitly requested

Current scope in this repo:

- model metadata and `SupportsXHigh`
- overflow detection
- message transformation for cross-provider replay
- minimal tool argument validation fallback

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
- Even after provider work starts, keep the provider set limited to `OpenAI OAuth` and `Kimi Coding` unless the user expands scope explicitly.
- Do not spend migration effort on local-model providers, local runtime adapters, or compatibility layers for self-hosted OpenAI-style endpoints unless the user asks for them explicitly.
- If behavior is unclear, derive expected behavior from tests before adding new abstractions.

## Testing

- Run the smallest relevant test set first, then broader tests as needed.
- When fixing a defect, add or update a regression test when the codebase allows it.
- If a failure cannot be reproduced locally, state that clearly instead of guessing.

## Communication

- Be direct about assumptions, tradeoffs, and breakage risks.
- If a requested approach conflicts with code health, explain the conflict briefly and choose the technically sound path unless the user explicitly wants otherwise.
