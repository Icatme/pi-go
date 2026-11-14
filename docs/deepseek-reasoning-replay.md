# Dedicated DeepSeek reasoning replay

The `deepseek-chat-completions` adapter preserves same-model thinking in
`reasoning_content`, separately from ordinary assistant `content`, following
[the official contract](https://api-docs.deepseek.com/guides/thinking_mode/).
Requests carrying tools require prior reasoning, including non-tool turns.
This is verified with offline HTTP fixtures, not live provider acceptance.

Existing transformation uses provider, API and requested model ID for identity;
`ResponseModel` remains diagnostic. Foreign thinking keeps the existing text
downgrade. Surviving non-redacted thinking blocks concatenate in original order
without trimming or added separators. Absent thinking stays absent; surviving
explicit empty signed blocks emit an empty field. Unsigned empty blocks still
follow the existing transform's omission. Redacted blocks are not converted to
clear text. Ordinary text joining and original history remain unchanged.

No change to generic Chat compatibility, settings, pricing or retries. This
cannot recover reasoning already absent from old histories.
