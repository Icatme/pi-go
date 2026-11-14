# Opt-in search description budgets

`codemode.SearchTools` retains full descriptions by default. Hosts can explicitly
choose `SearchOptions.DescriptionBudget = &SearchDescriptionBudget{MaxPerMatchBytes: 500, MaxTotalBytes: 2000}`.
Sandbox code can opt in per query:

```javascript
const hits = await searchTools("workflow status", {
  descriptionBudget: {maxPerMatchBytes: 500, maxTotalBytes: 2000}
});
text(hits);
// Load full guidance and schema before use when needed:
text(await describeTool(hits[0].name));
```

Both ceilings count only description UTF-8 bytes, not total serialized JSON,
tokens, or provider context. Names, namespaces, scores and ordering remain intact.
Search ranks the original complete text and selects the normal result count first.
Description prefixes are then allocated in rank order, without splitting UTF-8
runes. Remaining bytes can be used by later results. Combining-character/grapheme
boundaries and semantic completeness are not guaranteed. There is no inserted
ellipsis: `descriptionTruncated: true` explicitly marks a shortened description.
An originally empty description is not marked. `describeTool` still returns the
full authorized catalog entry. Before execution, read the full contract when guidance
is truncated; important restrictions may be in the omitted suffix. A short description
is not authorization. Catalog data is never changed.

Nil Go budget or absent sandbox option preserves existing search output, including no
new JSON marker. An explicit budget with zero ceilings deliberately omits nonempty
descriptions; negative limits are errors. The sandbox requires both integer keys;
null, missing keys, extra keys and invalid numeric types are rejected. Large
positive ceilings do not enlarge catalog admission limits or allocate buffers.

This per-call presentation option is NOT a permission boundary or mandatory host
resource limit. A script can omit it. Existing output limits still reject a whole
oversized output item; JSON escaping and metadata can exceed these description-only
budgets. The Agent toolset discovery wrapper does not automatically enable this
option. No automatic summarization, provider call, token-saving measurement or
retry behavior is added. Public struct additions may require downstream callers
using unkeyed literals to update them. The default codemodetool discovery guidance
now advertises this optional argument; custom tool descriptions remain host-controlled.
