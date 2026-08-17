---
name: Feature request
about: Propose a new endpoint, domain field, validation rule, or service behaviour
title: '[FEAT] '
labels: enhancement
assignees: ''
---

## Problem / motivation
What problem does this solve? Which callers or use cases are affected (tender-admin UI, Tender Service, AuthZ Enrichment, the tenant-offboarding consumer)?

Note: this service is explicitly interim (ADR-0007 Option D — scheduled to merge into the Tender Service, see `MIGRATION_RUNBOOK.md`). Prefer minimal, near-literal changes over new abstractions that would need unwinding at the Wave 4 merge (TAC-D1).

## Proposed solution
Describe the API or behaviour change you'd like.

```
// Example: new endpoint, request/response shape, or domain type
```

## Affected areas
- [ ] Public HTTP API (new or changed TAC-1..3 endpoint)
- [ ] Internal HTTP API (TAC-4 authorization check)
- [ ] Domain model (new field or entity in `internal/core/domain`)
- [ ] Database schema (new migration required)
- [ ] Event contract (this service only ever *consumes* `TenantOffboarded` — it publishes zero events per TAC-EVT-1/TAC-D5; a change here should not introduce a producer)
- [ ] Cache strategy (`tac:acl` TTL or invalidation)
- [ ] Membership-check client (`internal/adapter/outbound/membershipcheck`)
- [ ] Helm / deployment config

## Alternatives considered
Other approaches you evaluated and why you ruled them out.

## Acceptance criteria
- [ ]
- [ ]
- [ ]

## Additional context
Links to related issues, the LLD (`tender-acl-service-lld.md`), or prior art.
