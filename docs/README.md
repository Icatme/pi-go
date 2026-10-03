# Docs

This directory holds repository documentation that does not need to live at the
module root.

Current layout:

- `../pkg/pigo`: exported library package
- `../README.md`: root project overview
- `../AGENTS.md`: repo working rules for agents
- `pi-1-followup.md`: shipped Pi 1.0 scope, local first-round progress and the remaining follow-up plan

Operational notes:

- keep the package surface focused on the built-in providers listed in the root README
- keep provider-agnostic semantics stable before widening provider behavior
- do not add local-model or generic OpenAI-compatible support unless requested
