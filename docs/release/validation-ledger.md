# 1.0 validation ledger

One row per validated feature parent. Add or update the row when the feature acceptance criteria
pass. Change `validated` to `revalidate` when a later change plausibly affects the feature. The
release owner maintains this file; validation evidence and the ledger update happen together.

Every identity is immutable: use commits, tags, build numbers, artifact IDs, and container
digests, never "latest." Use `n/a` with a reason when a repository or artifact does not apply.
The linked evidence contains the full environment, validator, procedure, result, and known
limitations described in [1.0-readiness.md](1.0-readiness.md).

Owning paths are repository-relative **directory or file prefixes**, separated by commas. They
are not glob expressions. The initial server workflow reads only `Server prefixes`; the other
columns support equivalent repository checks and cross-repository review.

| ID | Feature | Issue | Status | Server identity | Apple identity | Android identity | Other identities | Corpus | Date | Evidence | Server prefixes | Apple prefixes | Android prefixes | Website prefixes |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|

<!-- Example row:
| FEATURE-REQUESTS | Requests | Silo-Server/silo-server#NNN | validated | v1.0.0-rc.1 / abc1234 / sha256:... | apple commit + build | android commit + build | website/plugin commits or n/a | corpus-v1 | 2026-09-05 | issue comment URL | internal/requests/, web/src/features/requests/ | Sources/Requests/ | app/src/main/.../requests/ | public/milestone/1.0/ |
-->
