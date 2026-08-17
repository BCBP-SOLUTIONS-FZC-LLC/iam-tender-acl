---
name: Bug report
about: Report a defect in the iam-tender-acl service (HTTP API, membership check, event consumption, caching, or database layer)
title: '[BUG] '
labels: bug
assignees: ''
---

## Description
A clear description of the bug.

## Service version
`github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl` — tag / commit SHA:

## Go version
`go version goX.Y.Z ...`

## Environment
- [ ] Local dev (`make run`)
- [ ] Docker Compose (`make compose-up`)
- [ ] Staging
- [ ] Production

## Affected area
- [ ] Public HTTP API (endpoint: `METHOD /api/v1/tenants/:id/tenders/:tender_id/acl...` — TAC-1/2/3)
- [ ] Internal HTTP API (`GET /internal/tenants/:id/tenders/:tender_id/acl/:user_id` — TAC-4)
- [ ] Membership-check client (`internal/adapter/outbound/membershipcheck` → iam-org-membership's `/internal/tenants/:id/members/:user_id/exists`)
- [ ] TenantOffboarded consumer (tenant-lifecycle-tenderacl-q)
- [ ] Cache (Valkey — `tac:acl:{tenant}:{tender}:{user}`)
- [ ] Database / migrations / RLS

## Steps to reproduce
1.
2.
3.

## Expected behaviour
What you expected to happen.

## Actual behaviour
What actually happened. Include error messages, HTTP status codes, log output, or stack traces.

```
// paste relevant log output or error here
```

## Minimal reproduction
```
// paste the smallest curl command, request payload, or Go snippet that triggers the bug
```

## Additional context
Any other relevant context (Postgres version, whether RLS was involved, whether the membership-check call failed open or closed, tenant/tender IDs if not sensitive, related issues).
