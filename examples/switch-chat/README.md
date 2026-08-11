# switch-chat

`switch-chat` is an example CLI for `pi-go/agent` that demonstrates:

- built-in preset switching
- per-preset local session persistence
- explicit reuse of `pi-go` credentials through `--auth-root`
- separate `chat` and `reflection` runtime modes

## Usage

```powershell
Set-Location examples
$env:GOWORK='off'
$switchChatExe = Join-Path $env:TEMP 'pi-go-switch-chat.exe'
go build -o $switchChatExe ./switch-chat
& $switchChatExe --auth-root 'C:\path\to\pi-go' --data-dir '.\switch-chat\.data'
```

Common flags:

- `--auth-root C:\path\to\pi-go`: points to the directory that contains `.pigo/auth.json` and `.pigo/.env`
- `--provider openai-codex`
- `--model gpt-5.4`
- `--data-dir .\switch-chat\.data`
- `--preset chat`
- `--reflection-max-turns 3`

Available commands:

- `/agents`
- `/use <preset>`
- `/show`
- `/reset`
- `/exit`

## Presets

- `chat`: general-purpose chat
- `coder`: code-focused chat
- `reflect`: single-run reflection mode

`reflect` uses a generator model and a model-backed structured evaluator. Every
draft, including the final allowed draft, is evaluated once. The critic must
return exactly one JSON object with no code fence or surrounding text:

```json
{
  "verdict": "accept",
  "summary": "The draft satisfies the complete request.",
  "revision_instructions": []
}
```

`verdict` is exactly `accept` or `revise`. An accepted draft has no revision
instructions; a draft marked for revision has at least one non-empty
instruction. Invalid, contradictory, truncated, or malformed critic output is
an error rather than a keyword-based decision. A revision pass retains the
complete original request, including prior turns and images, and adds the prior
draft plus the structured revision instructions.

The loop stops when a draft is accepted or after `--reflection-max-turns`
generation/evaluation pairs. Reflection mode intentionally does not restore a
live runtime snapshot; the example persists only its visible user/final-draft
transcript.
