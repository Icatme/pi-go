# Offline management catalog observations

`parseKiroOfflineModelCatalog` parses caller-supplied ListAvailableModels JSON;
there is no HTTP, credentials, profile selection, environment/cache read or write,
registry update, static bootstrap list or assertion of actual account availability.
The result is a separate observation type, **not** a runnable `Model`.

The parser keeps ordered model records, exact service IDs, a community-compatible
numeric-dot alias (`sample-2.3` → `sample-2-3`), optional display name, optional
positive int64 input/output limits, and raw request-fields schema. Alias collisions
fail the entire catalog. Exact service IDs must remain the request-time authority;
a normalized alias alone is not a license to guess the service ID.

Missing limits stay nil, missing names stay empty, null schema is absent. Full raw
catalog and per-model metadata retain unknown fields and numeric literals without
float conversion. All output buffers are independent of the input and one another;
callers must not mutate input concurrently. Retained raw metadata may be sensitive:
do not log it indiscriminately or mistake it for authenticated/provenanced evidence.
A schema is stored as an opaque JSON object, not validated as a particular JSON
Schema dialect, interpreted as a capability or executed.

The whole input is bounded to a caller limit of at most 1 MiB and model count to
at most 1024; parser nesting/duplicate-key/Unicode safeguards are reused. Retained
copies can consume more than input bytes; this is not an RSS bound. Invalid JSON,
empty catalogs, malformed records/limits/schema, invalid labels and collisions
return a zero snapshot, never a prefix. Unknown fields remain in raw evidence.

## Upstream scenario mapping (independently authored)

Pinned community revision: `dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba`.

- [management.test.ts L159–189](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/test/management.test.ts#L159-L189): retain the supplied catalog shape/metadata; only the response-data part is migrated, not the authenticated GET.
- [models.test.ts L193–232](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/test/models.test.ts#L193-L232): fresh schema/token metadata, null schema, invalid schema rejection and exact service ID preservation.
- Alias collision checks follow the observed `mapKiroCatalogModels` implementation; ambiguity, ownership, integer-precision and budget cases are independent Go hardening tests, not claimed upstream cases.

Deliberate differences: limits must be integer JSON literals (no fractions,
exponents or string coercion); labels are bounded and control-free, explicitly
invalid display names reject rather than silently fallback. No family-name
reasoning guesses, vision assumptions, humanized fallback names, default context
or max tokens, zero-cost pricing, effort-ladder inference, region/profile probes,
account cache or management retries are implemented here. A future host-model
projection needs explicit support/presence/capability policy and account-scoped
provenance; absence must not become a false capability/price/entitlement claim.
