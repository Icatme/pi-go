# Failed-generation tool results: audit versus model projection

The agent runtime records a synthetic error tool result when an assistant generation fails after exposing a tool call. This records that the call did **not** execute. It is not a provider-valid result once the failed assistant is omitted from provider history.

New results from this exact runtime path carry `ToolResultPayload.Provenance = "generation_failed_unexecuted"`. The field belongs to the stored message payload, not the `ToolResult` value returned by a tool. Snapshot clones, message events, JSON persistence and restored history retain the audit result.

The built-in agent-to-pigo model projection omits that result only when:

- Its current preceding assistant is Error or Aborted, without an intervening user/assistant round.
- That assistant has one unambiguous call with the same ID, original ID, and name. Duplicate IDs, including different names, are ambiguous.
- The result has the exact provenance, `IsError` is true, and it has no execution metadata.

A System message does not start a new tool round. A new user or assistant resets the matching scope. This prevents a failed call's ID from suppressing a successful later round that reuses it. The audit snapshot and events are never pruned by this projection.

## Compatibility boundaries

- Older records without provenance remain unchanged. No inference uses error text, `IsError` alone, or a global set of IDs. Already-corrupt old provider histories require an explicit host recovery policy.
- Ordinary tool errors, execution metadata, unknown provenance values, unmatched or ambiguous results remain in the projection. This patch does not claim to repair all invalid imported histories.
- Custom models and custom `ConvertToLLM` hooks still receive the audit-facing agent history under their existing contract. A custom serializer must apply its own explicit model-history policy; the low-level pigo `TransformMessages` API is unchanged.
- This is structured runtime provenance, not a cryptographic authenticity guarantee for arbitrary caller-supplied JSON. Hosts must continue treating imported snapshots according to their trust boundary. It does not authorize tool execution or retries.
