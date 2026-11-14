# Repeated foreign tool-call IDs in Responses replay

The ordinary `openai-responses` request builder now gives repeated foreign tool-call IDs unique, deterministic IDs when replaying complete stateless history. This addresses [upstream #10106](https://github.com/earendil-works/pi/issues/10106): different completed calls on a provider with a small ID space can collide when moved to Responses.

The [OpenAI Responses API reference](https://developers.openai.com/api/reference/resources/responses/methods/create) describes `call_id` as the unique call identifier and uses it to associate the output. Checked 2026-11-14; this is not a claim about uniqueness across server-side response chains.

## Scope

- Only IDs whose occurrences all come from non-Responses APIs are candidates. Native Responses/Codex and mixed native/foreign collisions are unchanged, not claimed repaired.
- Every affected call must be in a completed tool-use assistant message with exactly one immediately following matching result. Multiple tools in a round can have distinct IDs and results in any order. Ambiguous same-round normalized IDs, missing/duplicate results, and name mismatches are rejected before HTTP rather than guessed.
- Two complete sequential calls may have identical names, arguments and outputs. They remain two calls; nothing is deduplicated or executed.
- Later occurrences receive bounded `call_pigo_replay_N` IDs, avoiding all original normalized call and result IDs, including later items. Outputs are renamed with their own call. History and signatures are not edited in place, and ordering is unchanged.
- A history that needs this repair cannot be combined with `previous_response_id`: the builder returns an explicit error rather than rewriting server-side references. Unique-ID histories with that option are unaffected.
- The Codex builder is not changed. This is not universal history sanitization, encrypted-item repair, or orphan-result removal.

Validation uses offline serialized requests and localhost fixtures only, not a live provider acceptance test.
