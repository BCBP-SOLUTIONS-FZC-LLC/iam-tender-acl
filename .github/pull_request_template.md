
## Description
Provide a clear description of the changes.

---

## Type of Change
- [ ] Bug fix
- [ ] New feature
- [ ] Refactor
- [ ] Documentation
- [ ] Test
- [ ] Breaking change
- [ ] New migration
- [ ] Event contract change (`api/asyncapi.yaml` — the TenantOffboarded schema this service consumes)

---

## Testing
- [ ] Unit tests added/updated (`make test-unit`)
- [ ] RLS integration tests added/updated (`make test-rls`)
- [ ] Integration tests added/updated (`make test-integration`)
- [ ] End-to-end tests added/updated (`make test-e2e`)
- [ ] All tests passing with race detector (`make race`)
- [ ] Manual testing performed (if required)

---

## Checklist

### Code Quality
- [ ] Code is properly formatted (`make fmt-check`)
- [ ] Linting passed (`make lint`)
- [ ] Vet passed (`make vet`)
- [ ] Architecture lint passed (`bash .github/scripts/arch-lint.sh` / `go-arch-lint check`) — Clean Architecture layering enforced: `core/domain` -> `core/port` -> `core/service`, adapters depend only on core, never on each other (see `.go-arch-lint.yml`)
- [ ] No debug logs / commented-out code
- [ ] No secrets or DSNs hardcoded

### API Contract
- [ ] Swagger docs regenerated if handler annotations changed (`make swag` — all three files in `docs/swagger/` committed)
- [ ] `api/asyncapi.yaml` updated if the consumed `TenantOffboarded` payload changed

### Database / Migrations
- [ ] New migrations have matching `.up.sql` and `.down.sql`
- [ ] Down migration correctly reverses the up migration
- [ ] RLS policies tested with `FORCE ROW LEVEL SECURITY` (`make test-rls`) — including the fail-closed (missing GUC) and no-leak-across-pooled-connection cases
- [ ] TAC-2 (grant) and TAC-3 (revoke) writes remain single-row, transactional, and match the documented concurrency model (TAC-3's `record_version` optimistic-lock check; TAC-2 has none, per `IMPLEMENTATION_GAP_ANALYSIS.md`)

### Security
- [ ] No secrets or DSNs hardcoded
- [ ] The runtime app role (`tender_acl_app`) is never granted `BYPASSRLS`, and no migration accidentally widens its privileges
- [ ] `app.tenant_id` is only ever bound via `pgcommon.WithTenantTx` (`SELECT set_config(..., true)`) — never a session-level `SET`
- [ ] The `membershipcheck` client fails **closed** on TAC-2 (grant blocked, not defaulted) per LLD §7.6.2/TAC-FAIL-1 — verify any change here doesn't flip this to fail-open
- [ ] New config fields documented in `.env.example` and `cmd/tender-acl/config.go`'s `loadConfig`

### Documentation
- [ ] README updated (if public API, env vars, or deployment topology changed)

---

## Related Issue
Closes #<issue-id>

---

## Deployment Notes
Mention anything important for operators upgrading (migration steps, new required env vars, config changes, breaking event schema changes).
