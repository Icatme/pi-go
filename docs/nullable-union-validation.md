# Preserve explicit null in nullable unions

A null input remains null when it already satisfies an `anyOf` member or exactly
one `oneOf` member. Previously a leading number/string/boolean arm could coerce
it to zero/empty-string/false, or cause a valid nullable `oneOf` to be rejected.
A null matching multiple native `oneOf` members is still rejected.

Only actual null values receive this native-match probe. Non-null branch-order
coercion is unchanged, including `anyOf: [string, number]` converting 42 to "42".
If no member accepts null natively, existing coercion remains. Nested nullable
values in object properties, additional properties and arrays use the same rule.

The probe uses the existing lightweight validator subset with coercion disabled
recursively; it is not a full JSON Schema implementation. Unsupported keywords,
`$ref` behavior and existing composition limitations remain. The Agent's separate
resolved-schema validation still applies. Previously persisted coerced values
cannot be reconstructed. Tests include actual RunToolCall execution, nullable
constraints, nested values, oneOf ambiguity and original non-null behavior.
