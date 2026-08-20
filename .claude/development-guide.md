# Development guide

## Key design decisions (decision register, LLD §23)

| # | Decision |
|---|---|
| TAC-D1 | **Superseded.** Originally a deliberately minimal, near-literal-lift flat package layout (explicitly interim). Later reversed to the same Clean Architecture layering every sibling service uses — see `ARCHITECTURE.md`'s "Layer model" and `CHANGELOG.md`. The interim/disposable framing is unchanged, only the package structure. |
| TAC-D2 | Lost composite FK to `tenant_memberships` replaced by a synchronous grant-time-only membership check, behind a swappable `port.MembershipCheckClient` interface for a cheap Wave-4 repoint/delete. |
| TAC-D3 | TAC-1/TAC-4 need no membership join, no cross-service call — only TAC-2 does. |
| TAC-D4 | FK loss is low-risk because the active-grant predicate (TAE-3) was never conditioned on live membership status; AuthZ Enrichment's I-8 gate provides read-time safety independently. |
| TAC-D5 | No new event introduced for grant/revoke — preserves the source's "no bus event" posture. |
| TAC-D6 | Wave-4 entry criteria written down now, per ADR-0007 Action Item 7 (LLD §22). |
| TAC-D7 | Second FK loss (`fk_tae_tenant ON DELETE CASCADE`) replaced by an async SQS subscription, not a second synchronous check — mirrors Wave-2's GM-D2. |
| TAC-D8 | No `rls_check_tenant()`/`rls_violation_log` forensic-logging wrapper — deliberate scope reduction, mirrors Wave-2's GM-D8. |
| TAC-D9 | This LLD revision adopts the canonical section template and repoints broken cross-references from earlier drafts; requirement IDs preserved, only relocated. |

## Extending the service

Because this service is explicitly interim (scheduled to merge into the Tender Service at Wave 4),
**resist adding abstractions** beyond what's already here. Before adding anything, ask: does Wave
4 actually need this to be a separate, swappable piece, or is it scope creep on a service that's
supposed to be as thin as possible?

- **A new grant field** → add the column via a new migration, extend `domain.TenderACLEntry`,
  update `ACLService.Grant`'s validation, update the Swagger annotations + regenerate (`make
  swag`), and add a revision-history entry to `docs/lld/iam-lld-tender-acl-service.md`.
- **A new inbound event** → add the message + schema to `api/asyncapi.yaml` (with the correct
  `consumed` tag), wire a new `events.NewSQSConsumer`/handler in `main.go`, and decide whether it
  shares the existing `processed_events` ledger (new `consumer` value) or needs its own idempotency
  store.
- **Never add an outbound event/outbox** without revisiting TAC-D5 explicitly — this service's
  entire event posture is "consumes two, publishes zero."
- **Never repoint `port.MembershipCheckClient` casually** — it exists specifically so Wave 4 can
  cheaply delete or replace it; keep the interface minimal (`Exists` only).

## Development workflow

1. `make setup` once, then `make docker-up` (Postgres + Valkey + LocalStack via
   `docker-compose.yml`).
2. `make run` — sources `.env` automatically, runs the binary against the compose stack.
3. Iterate; run `make test` (unit) frequently, `make test-integration`/`test-rls`/`test-e2e` before
   pushing.
4. `make lint`/`make vet`/`make fmt-check` — the pre-commit hook (`make install-hooks`) runs `tidy
   && fmt-check && vet && lint` automatically on every `git commit` anyway.
5. If you touched a handler's `// @…` annotations, run `make swag` and commit the regenerated
   `docs/swagger/` output (`make swag-check` is the CI drift gate). If you touched
   `api/asyncapi.yaml`, sanity-check `/asyncapi` renders correctly (`make run`, then open
   `http://localhost:8086/asyncapi`).

Branching: never commit directly to `main` — branch first. Update `CHANGELOG.md`'s `[Unreleased]`
section for any user-visible or architecturally-relevant change; the `changelog-check.yml` CI
workflow gates on this.

## Troubleshooting

| Symptom | Likely cause / fix |
|---|---|
| `503 core_unavailable` on every Grant | `CORE_INTERNAL_BASE_URL` unreachable or `iam-org-membership`'s internal exists-endpoint down/missing — check `O_AND_M_DELTA.md` for endpoint status. |
| `409 optimistic_lock_conflict` on Revoke | Caller's `record_version` is stale — re-fetch via TAC-1 and retry with the current value; this is by design, not a bug. |
| `409 duplicate_grant` on Grant | An active (non-revoked) grant already exists for that `(tenant, tender, user)` — TAE-1's partial unique index is doing its job; revoke first if replacing it. |
| TAC-4 returns stale `has_access` briefly after a Revoke | Expected — up to the time between commit and the cache `DEL` call, bounded by the 30s TTL as a hard ceiling. Not a bug unless it persists past 30s. |
| `/readyz` reports `postgres_pool` degraded but pod stays up | Check `checks.postgres_pool.utilization` — if near 1.0, raise `PG_MAX_CONNS` or investigate a connection leak; the pod isn't failed out solely for pool pressure. |
| A `TenantOffboarded`/`TenantMembershipRemoved` event seems to run twice | Check `processed_events` for that `(event_id, consumer)` — if absent both times, the idempotency ledger itself may be down (check `checks.postgres`); if present, this is expected SQS at-least-once delivery being correctly deduped, not a bug. |
| `go-arch-lint` fails after a new import | You've introduced a dependency `.go-arch-lint.yml` doesn't allow (e.g. `core/service` importing an adapter directly) — fix the import direction, don't add an exception without a real architectural reason. |
| gopls reports a stale `go.mod` error after `make tidy` | Restart the language server — verify first with `go list -m all` / `go mod edit -fmt` that `go.mod` is actually valid before assuming a real problem. |

## Design patterns to follow

- Every service method starts its own `tracer.Start(ctx, "service.ACLService.<Method>")` span —
  match this in any new method.
- Cache invalidation and cache population failures are **always** `WarnContext`-logged and
  swallowed, never propagated as request errors — match this for any new cache interaction.
- Fail closed, not open, on any synchronous outbound dependency error — this is the one rule that
  must never be relaxed for TAC-2's membership check, and should be the default assumption for any
  future synchronous call this service adds.
- Idempotency for consumers is check-before/mark-after against `processed_events`, keyed by
  `(event_id, consumer)` — reuse this pattern (with a new `consumer` value) rather than inventing a
  new dedup mechanism per consumer.

## Appendix — error codes

| Code | HTTP status (via `respondACLError`) | Raised by |
|---|---|---|
| `invalid_request` | 400 | generic request validation |
| `unauthorized` | 401 | missing/invalid gateway identity headers |
| `insufficient_role` | 403 | role check on TAC-1/2/3 |
| `invalid_access_level` | 400 | Grant: `access_level` not one of view/edit/approve |
| `invalid_reason` | 400 | Grant: `reason` > 500 chars |
| `invalid_expiry` | 400 | Grant: `expires_at` not in the future |
| `grantee_not_active_member` | 422 | Grant: membership check returned not-active |
| `optimistic_lock_conflict` | 409 | Revoke: `record_version` mismatch |
| `duplicate_grant` | 409 | Grant: active grant already exists (unique index violation) |
| `core_unavailable` | 503 | Grant: membership check errored/timed out |
| `dependency_unavailable` | 503 | Postgres/Valkey connectivity failure |
| `internal_server_error` | 500 | unclassified failure |

---
Document reflects `iam-tender-acl` as of 2026-08-20 (ADR-0007 Wave 3, Phase 1 complete).
