# Required/nullable tool schemas

`strictschema.New` prepares a **detached** declaration for a host transport that
requires all properties to be present. Originally optional properties become
nullable. After validating against `Parameters()`, call `Normalize` before
executing the original business tool. It removes only those optional nulls,
including in nested objects and arrays; empty values, zero, false and required
nulls remain intact. Numeric lexemes are preserved.

```go
prepared, err := strictschema.New(originalParameters, strictschema.Options{
    MaxSchemaBytes: 8192,
    MaxArgumentBytes: 32768,
})
// Handle err before exposing the tool.
wireParameters := prepared.Parameters()
// Validate model arguments against wireParameters using the ordinary runtime.
businessArguments, err := prepared.Normalize(validatedArguments)
// Handle err, then invoke the original tool with businessArguments.
```

This helper does not turn on provider strict mode, select a provider, perform
catalog I/O, grant permissions or validate all JSON Schema constraints.
`Normalize` checks shape; the ordinary schema validator and business validation
remain required. The host decides which incompatible external tools to omit
from its own catalog; one rejected declaration need not disable unrelated tools.

Supported shapes are closed objects with explicit properties, arrays with one
items schema, and scalar types. Scalar constraints and annotations are retained.
Open objects, references, unions, conditionals, optional explicit-null fields,
container enum/const and object property-count constraints are rejected. These
cannot be passed through this omission convention without changing semantics.
Unknown keywords are rejected rather than dropped. Schema and argument byte
bounds apply before and after conversion; zero options use bounded defaults.

Each call receives independent data. Source maps and maps returned by
`Parameters()` cannot mutate the compiled omission plan. The original execution
schema should stay pinned independently from the model-facing declaration.
