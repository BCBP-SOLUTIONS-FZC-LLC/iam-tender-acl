# Architecture diagrams

Standalone Mermaid source files for `ARCHITECTURE.md`. Each `.mmd` file is embedded as a fenced code block in the parent document with a `> Source:` back-reference.

| File | Diagram | Embedded in |
|------|---------|------------|
| [`layer-model.mmd`](mermaid/layer-model.mmd) | Clean Architecture / Ports-and-Adapters package layout (TAC-D1 superseded — see ARCHITECTURE.md) | ARCHITECTURE.md §Layer model |
| [`package-dependencies.mmd`](mermaid/package-dependencies.mmd) | Go import graph — the Clean Architecture dependency rule enforced by go-arch-lint | ARCHITECTURE.md §Package dependency graph |
| [`request-flow.mmd`](mermaid/request-flow.mmd) | HTTP request lifecycle (role check, membershipcheck hop on TAC-2, RLS) | ARCHITECTURE.md §Request flow |
| [`event-consumer-flow.mmd`](mermaid/event-consumer-flow.mmd) | SQS consume → idempotency check → cascade delete of tender_acl_entries (tenant-offboarding) | ARCHITECTURE.md §Event consumer flow |
| [`member-removal-flow.mmd`](mermaid/member-removal-flow.mmd) | SQS consume → idempotency check → soft-delete of tender_acl_entries for one user (ADR-0007 Wave 3 Phase 3) | ARCHITECTURE.md §Per-user-removal cascade flow |
| [`cache-strategy.mmd`](mermaid/cache-strategy.mmd) | TAC-4's single cache key, 30s TTL, read/write/invalidation paths | ARCHITECTURE.md §Cache strategy |
| [`rls-guc-flow.mmd`](mermaid/rls-guc-flow.mmd) | RLS GUC injection per transaction, fail-closed | ARCHITECTURE.md §Row-Level Security |

There is no `reconcile-write-flow.mmd` here (this service has no full-replacement reconcile algorithm — TAC-2/TAC-3 are simple single-row grant/revoke) and no `observability-stack.mmd` (OTel/Prometheus/structured-log wiring is identical to the sibling services and not repeated diagrammatically here — see ARCHITECTURE.md §Observability for the prose version).

To render locally, open any `.mmd` file in a Mermaid-aware IDE (VS Code + Mermaid Preview, IntelliJ + Mermaid plugin) or paste into [mermaid.live](https://mermaid.live).
