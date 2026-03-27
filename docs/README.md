# Docs

This directory holds repository documentation that does not need to live at the
module root.

Current layout:

- `../pkg/pigo`: exported library package
- `../README.md`: root project overview
- `../AGENTS.md`: repo working rules for agents

Operational notes:

- keep the package surface focused on `openai-codex` and `kimi-coding`
- keep provider-agnostic semantics stable before widening provider behavior
- do not add local-model or generic OpenAI-compatible support unless requested
