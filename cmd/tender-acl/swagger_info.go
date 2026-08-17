// Package main global Swagger annotations. `swag init` reads this file
// (via `-g swagger_info.go`) to build the top-level OpenAPI/Swagger
// specification — title, version, host, security schemes, and tag
// descriptions. Per-handler `// @…` annotations live next to each handler
// function under internal/adapter/inbound/http/.
//
// Mirrors iam-org-membership's identical pattern so developers moving
// between services see the same authoring workflow. Regenerate the spec
// with:
//
//	make swag
//
// The generated files under docs/swagger/ are checked into the repo;
// CI's `make swag-check` fails a PR whose annotations diverge from them.
//
// No @BasePath is set: TAC-1/2/3 live under /api/v1 but TAC-4 is
// registered without that prefix (LLD §8.3 — a mesh-only route, not a
// tenant-facing one), so every @Router annotation below is a full,
// absolute path rather than relative to a shared base.
//
// @title           Tender ACL Service API
// @version         1.0
// @description     Restricted-tender access overlay, extracted from iam-org-membership per ADR-0007 Wave 3. Owns exactly one resource — tender_acl_entries — behind three admin-gated grant/revoke/list endpoints and one mesh-only authorization check.
// @description
// @description     **Route prefixes.**  `/api/v1/*` — tenant-admin callers (`tender_admin`/`tenant_admin`/`tenant_owner`, via `x-tenant-roles`).  `/internal/*` — in-mesh service-to-service, mTLS trust boundary only, no RBAC/JWT check at all (LLD §8.2/§13.2).
// @description
// @description     TAC-4 (the internal check) never returns 404 — a missing grant is a valid, cacheable `has_access:false` answer, not an error.
//
// @contact.name   BCBP Solutions
//
// @license.name   Proprietary
//
// @securityDefinitions.apikey UserID
// @in                         header
// @name                       x-user-id
// @description                Authenticated user UUID injected by the API gateway. Required on /api/v1/* routes only — /internal/* is mesh-only and carries no identity headers.
//
// @securityDefinitions.apikey TenantID
// @in                         header
// @name                       x-tenant-id
// @description                Tenant UUID injected by the API gateway. Must match the :id path parameter (defense-in-depth alongside RLS) on /api/v1/* routes.
//
// @securityDefinitions.apikey TenantRoles
// @in                         header
// @name                       x-tenant-roles
// @description                Comma-separated tenant-role list injected by the API gateway. /api/v1/* routes require tender_admin, tenant_admin, or tenant_owner.
//
// @tag.name         public
// @tag.description  Tenant-admin grant/revoke/list (TAC-1, TAC-2, TAC-3)
//
// @tag.name         internal
// @tag.description  Mesh-only authorization check (TAC-4/I-12) — no RBAC
//
// @tag.name         infra
// @tag.description  Health checks (unauthenticated)
package main
