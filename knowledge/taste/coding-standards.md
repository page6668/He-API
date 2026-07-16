# taste/coding-standards

Rules `implement` and `review-code` read as their taste slice. Terse rules, each
with provenance. Not prose. Seed examples below — replace with this project's.

| id            | rule                                                                | rationale                                  | source | added      | approved_by |
| ------------- | ------------------------------------------------------------------- | ------------------------------------------ | ------ | ---------- | ----------- |
| ts-strict     | TypeScript strict mode on; no `any` without an inline justification | Catches runtime errors at compile time     | human  | 2026-06-27 | dorayo      |
| named-exports | Named exports only; no default exports                              | Refactor-safe imports, better autocomplete | human  | 2026-06-27 | dorayo      |
| no-swallow    | Never swallow errors; handle or rethrow with context                | Silent failures are undebuggable           | human  | 2026-06-27 | dorayo      |
| pure-io-split | Keep I/O at the edges; core logic pure and testable                 | Testability, fewer mocks                   | human  | 2026-06-27 | dorayo      |

<!--
Metabolism: when a human corrects taste at the accept gate, append or supersede a
row here (source: <skill or human>, with date + approver). Supersede, don't
delete — keep what the org learned visible.
-->
