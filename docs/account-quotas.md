# Account quota queries

[Documentation index](README.md) · [Provider guide](providers.md)

Account balances and usage windows are an optional provider capability,
independent of model generation and per-request token usage. `commandcode` is
currently the only built-in provider with quota queries. Check support without
network access with `pigo.SupportsQuotaQuery(provider)`; unsupported providers
return `pigo.ErrQuotaUnsupported` from `pigo.QueryQuota`.

```go
import (
    "context"

    "github.com/Icatme/pi-go/pkg/pigo"
)

func accountQuota(ctx context.Context, auth map[pigo.Provider]pigo.AuthConfig) (pigo.QuotaResult, error) {
    return pigo.QueryQuota(ctx, "commandcode", pigo.QuotaQueryOptions{Auth: auth})
}
```

## Credentials and request limits

`QuotaQueryOptions.APIKey` overrides `Auth`. When neither an API key nor a
provider auth entry is supplied, Command Code uses its normal
environment/user-home lookup.
`BaseURL` supplies an explicit trusted account API endpoint, and `HTTPClient`
allows a caller-owned transport. Pass a context deadline to bound the complete
query; otherwise it has a 15-second timeout. Queries do not refresh models,
generate text, retry requests, or cache account snapshots.

## Reading a result

`QuotaResult` separates balances, rolling windows, subscription details, and
unavailable sections. Amounts include a unit; a nil amount or timestamp means
unknown, while a non-nil zero is a known normalized amount, including a
depleted balance or exhausted window. Window remaining amounts are headroom
under independent limits and must not be added to account
balances. Different units must not be combined either. A window's optional
`BalanceIDs` identifies the pools constrained by that window; nil means its
pool scope is unreported or it is not a balance limit. A usable partial result
has `Unavailable` issues and a nil error; authentication failures and invalid
required responses return an error.

## Adding a provider adapter

A provider adds this capability by assigning a `QuotaQuerier` implementation to
`ProviderModule.Quota`. Its `QueryQuota(context.Context, QuotaQueryOptions)`
method returns the normalized snapshot and must support concurrent read-only
calls. The contract follows cc-switch's explicit optional amounts and multiple
usage entries ([reference source](https://github.com/farion1231/cc-switch/blob/0d0955550323f72ee8f217bd5d1857c659b2161e/src-tauri/src/provider.rs#L136));
provider adapters in pi-go use typed Go implementations.

## Command Code behavior

The Command Code adapter queries `/alpha/whoami`, `/alpha/billing/credits`, and
`/alpha/billing/subscriptions` on its account API. These are the alpha endpoints
used by the Command Code CLI; upstream changes can make sections unavailable.
It reports three separate pools (`monthly`, `purchased`, and `free`), with each
remaining amount clamped to zero when the upstream balance is negative. The
unit is provider `credits`; their value can vary by plan and model, so the
adapter does not convert them to USD. Five-hour and weekly windows identify
only `monthly` in `BalanceIDs`: purchased credits bypass those limits. See the
[Command Code usage policy](https://commandcode.ai/docs/resources/usage-limits).
The adapter does not create an aggregate balance. Monthly allowance and
spending remain unknown: it does not infer them from a plan name or query the
separate usage-summary endpoint.

## CLI

```powershell
pigo quota commandcode
pigo quota --json commandcode
```

Use `pigo quota [--json] <provider>` with flags before the provider. It reads
CLI-managed `.pigo/auth.json` through the existing auth bridge and then uses
the provider's normal credential lookup when no local entry is available.
Human output keeps balances and windows separate, with `applies-to` for known
window pool scopes; `--json` writes the full
`QuotaResult` to stdout. Unavailable sections emit warnings to stderr while
retaining a successful exit status for a usable snapshot. Fatal query errors
and output failures return a nonzero exit status.
