# Pi issue audit, 2026-11-13

Baseline: pi-go master `ae4e17f9022e40b17a1030824ce9a7a075ca610b`, fetched with git clone. The upstream `badlogic/pi-mono` redirects to `earendil-works/pi` (also named in AGENTS.md).

## Instruction role: upstream #12101

Both Chat and Responses reproduced the reported issue: an unknown compatible reasoning endpoint received developer by default, while explicit developer support was ignored on non-reasoning models.

Unknown compatible endpoints now default to system. Explicit SupportsDeveloperRole selects the role independently of reasoning. Known native OpenAI/Codex reasoning endpoints retain the established default, using exact host matching.

Go 1.26.5: regression fails on the original implementation and passes after the fix; full pkg/pigo ordinary tests, race, and vet passed. No live provider requests, deployment, or full application validation.

Upstream: https://github.com/earendil-works/pi/issues/12101
