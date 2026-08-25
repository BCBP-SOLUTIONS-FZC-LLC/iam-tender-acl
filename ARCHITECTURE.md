# Architecture

## Mental model

One table (`tender_acl_entries`), four endpoints, one synchronous outbound dependency, two inbound
async subscriptions (tenant-offboarding, and per-user-removal since ADR-0007 Wave 3 Phase 3), zero
outbound events. This service exists to run standalone just long enough to
be safely decoupled from `iam-org-membership`'s database — it is explicitly disposable, slated for
reabsorption into the Tender Service at Wave 4 (`tender-acl-service-lld.md` §22, ADR-0007 Option
D). Every design choice below optimizes for "correct and cheap to delete later," not "built to
last."

## Layer model — Clean Architecture / Ports-and-Adapters

This service uses the same Clean Architecture / Ports-and-Adapters split its siblings
(`iam-org-membership`, `iam-catalog-admin`, `iam-group-mapping`) use:
`internal/core/{domain,port,service}` + `internal/adapter/{inbound,outbound}`.

**History:** it originally shipped with a deliberately flat, near-literal-lift layout
(`tender-acl-service-lld.md` §6, decision TAC-D1) — the reasoning at the time was that this service
is explicitly interim (Wave 4 folds it away entirely, ADR-0007 Option D), so minimizing abstraction
seemed cheaper than building layering that would just be unwound in a future merge. That decision
was later reversed to bring this service in line with every sibling IAM service's structure and
tooling (shared `go-arch-lint` conventions, the same mental model for anyone moving between repos) —
see `CHANGELOG.md` for when. The interim/disposable framing in the Mental Model section above is
still accurate; only the package layout changed, not the "don't over-engineer, this gets folded into
the Tender Service eventually" posture.

```mermaid
graph TD
    subgraph cmd["Composition Root  —  cmd/tender-acl/"]
        main["main.go\nwire pgcommon.Pool + Valkey client + membershipcheck.HTTPChecker\nrouter registration · self-migrate at startup (migrate.go) · SQS consumer goroutine\ngraceful shutdown order: HTTP+metrics servers -> cancel errgroup ctx -> SQS Run returns"]
        config["config.go\nenv-driven config, fails fast on missing\nDATABASE_URL / SQS_QUEUE_URL\n(CORE_INTERNAL_BASE_URL, MIGRATION_DATABASE_URL default if unset)"]
    end

    subgraph domainpkg["internal/core/domain/  —  pure business types"]
        domain["acl.go — TenderACLEntry · TenderACLLevel (view/edit/approve) · IsActive()\nCachedAccess (TAC-4 result / cache wire shape)\nerrors.go — Error, error codes"]
    end

    subgraph portpkg["internal/core/port/  —  interfaces, no implementations"]
        repoiface["acl_repository.go — TenderACLRepository interface"]
        mciface["membershipcheck.go — MembershipCheckClient interface"]
        cacheiface["cache.go — Cache interface"]
    end

    subgraph servicepkg["internal/core/service/  —  business logic, depends only on domain+port"]
        aclsvc["acl_service.go\nACLService: List / Grant / Revoke / CheckAccess\nGrant calls port.MembershipCheckClient.Exists — fail closed (TAC-FAIL-1)"]
    end

    subgraph httpadapter["internal/adapter/inbound/http/  —  HTTP adapter"]
        handler["handler.go · internal_handler.go\nTAC-1 List · TAC-2 Grant · TAC-3 Revoke (public, role-gated)\nTAC-4 CheckAccess (internal, mesh-only, never 404)"]
    end

    subgraph consumeradapter["internal/adapter/inbound/consumer/  —  SQS adapter"]
        consumerimpl["OffboardingConsumer + MemberRemovalConsumer + ProcessedEvents\ntenant-lifecycle-tenderacl-q (tenant cascade) +\nmember-removal-tenderacl-q (per-user cascade, Wave 3 Phase 3)\ncheck-then-mark idempotency, independent consumer names\nCascadeDeleter/UserRemovalCascader/CascadeMetrics/MemberRemovalMetrics\ninterfaces declared LOCALLY, satisfied structurally — NO import\nof postgres or service packages"]
    end

    subgraph pgadapter["internal/adapter/outbound/postgres/  —  persistence adapter"]
        repo["repository.go — TenderACLRepository (pgx impl), incl. CascadeDeleteForTenant\nmigrations/ — schema, RLS, touch_row(), roles"]
    end

    subgraph valkeyadapter["internal/adapter/outbound/valkey/  —  cache adapter"]
        cacheimpl["Cache\ntac:acl:{tenant}:{tender}:{user} · fixed 30s TTL\nonly cache in this service (LLD §9) — TAC-1 is not cached"]
    end

    subgraph mcadapter["internal/adapter/outbound/membershipcheck/  —  outbound HTTP client"]
        mc_http["HTTPChecker — calls iam-org-membership's\nGET /api/v1/internal/tenants/:id/members/:user_id/exists\n300ms client timeout · fails closed on any error"]
    end

    subgraph tests["Tests  —  test/"]
        rls["rls/\nRLS policy enforcement (testcontainers)"]
        integration["integration/\ncross-layer tests incl. SQS-compatible container (testcontainers)"]
        e2e["e2e/\nfull HTTP-stack request flows (testcontainers)"]
    end

    main   --> httpadapter
    main   --> consumeradapter
    main   --> pgadapter
    main   --> valkeyadapter
    main   --> mcadapter
    main   --> servicepkg
    main   --> config

    handler --> aclsvc
    aclsvc  --> domainpkg
    aclsvc  --> portpkg

    pgadapter     -->|"implements"| portpkg
    valkeyadapter -->|"implements"| portpkg
    mcadapter     -->|"implements"| portpkg

    consumerimpl -.->|"structurally satisfied by, wired in main.go\n— no import edge"| repo

    rls         -.->|"imports"| pgadapter
    integration -.->|"imports"| pgadapter
    integration -.->|"imports"| consumeradapter
    e2e         -.->|"imports"| httpadapter
```
> Source: [`docs/architecture/mermaid/layer-model.mmd`](docs/architecture/mermaid/layer-model.mmd)

The three pinned shared libraries (`platform-gincommon`, `platform-events`, `platform-pgcommon`)
are fetched as private `github.com/BCBP-SOLUTIONS-FZC-LLC/*` Go modules — not vendored locally — the
same convention iam-org-membership and iam-user-profile use.

## Package dependency graph

```mermaid
graph LR
    main(["cmd/tender-acl/main.go"])

    domain(["internal/core/domain\n(TenderACLEntry, CachedAccess, domain.Error)"])
    port(["internal/core/port\n(TenderACLRepository, MembershipCheckClient, Cache interfaces)"])
    service(["internal/core/service\n(ACLService — business logic)"])
    obs(["internal/adapter/outbound/metrics\n(cross-cutting OTel instruments)"])

    httpadapter(["internal/adapter/inbound/http\n(handler + router + DTOs)"])
    consumeradapter(["internal/adapter/inbound/consumer\n(OffboardingConsumer + MemberRemovalConsumer + ProcessedEvents)"])
    postgres(["internal/adapter/outbound/postgres\n(TenderACLRepository impl)"])
    valkey(["internal/adapter/outbound/valkey\n(Cache impl)"])
    membershipcheck(["internal/adapter/outbound/membershipcheck\n(HTTPChecker impl)"])

    main --> httpadapter
    main --> consumeradapter
    main --> postgres
    main --> valkey
    main --> membershipcheck
    main --> service
    main --> obs

    port    --> domain
    service --> domain
    service --> port

    httpadapter --> service
    httpadapter --> port
    httpadapter --> domain
    httpadapter --> obs

    postgres        -->|"implements"| port
    valkey          -->|"implements"| port
    membershipcheck -->|"implements"| port

    consumeradapter -.->|"structurally satisfied,\nwired only in main.go"| postgres
    consumeradapter -.->|"structurally satisfied,\nwired only in main.go"| obs
```
> Source: [`docs/architecture/mermaid/package-dependencies.mmd`](docs/architecture/mermaid/package-dependencies.mmd)

**The dependency rule (enforced by `.go-arch-lint.yml`):** `core/domain` has no internal
dependencies; `core/port` depends only on `domain`; `core/service` depends only on `domain`+`port`;
adapters depend on `core` (never on each other directly). `internal/adapter/inbound/consumer` takes
Interface Segregation one step further: it declares its own minimal interfaces (`CascadeDeleter`,
`IdempotencyStore`, `CascadeMetrics`, `UserRemovalCascader`, `MemberRemovalMetrics`) locally, narrow
slices of `port.TenderACLRepository` and the metrics adapter's method set, rather than depending on
the full port interface or importing the outbound adapters directly — `main.go` wires the concrete
`*postgres.TenderACLRepository`/`*metrics.Metrics` into those narrow interfaces purely via Go's
structural typing, so `go-arch-lint` sees no import edge from `adapter/inbound/consumer` to
`adapter/outbound/postgres` at all.

## Request flow

```mermaid
sequenceDiagram
    participant Client
    participant GW as API Gateway
    participant MW as gincommon middlewares
    participant H as Gin Handler (internal/adapter/inbound/http)
    participant MC as port.MembershipCheckClient
    participant Core as iam-org-membership
    participant DB as PostgreSQL (RLS)
    participant Cache as Valkey (tac:acl)

    Client ->>+ GW: HTTP request (JWT already validated upstream)
    GW ->>+ MW: inject X-Tenant-Id / X-User-Id / X-Tenant-Roles headers
    Note over MW: otelgin.Middleware — OTel span<br/>RequestContextMiddleware — parse headers into RequestContext<br/>Recovery — panic -> 500 JSON, never crashes the process<br/>Logger — structured request log<br/>MetricsMiddleware — gincommon's own http_requests_total / _duration_seconds (passthrough, not a tender_acl_* duplicate)

    alt public admin endpoint (TAC-1 List / TAC-2 Grant / TAC-3 Revoke)
        MW ->>+ H: c.Next()
        Note over H: 403 insufficient_role unless caller holds<br/>tender_admin / tenant_admin / tenant_owner for the path tenant
    else internal authorization check (TAC-4)
        MW ->>+ H: c.Next()
        Note over H: no role check — mTLS-only network path,<br/>tenant isolation still enforced by RLS alone,<br/>runs under the TARGET tenant's GUC
    end

    alt TAC-2 Grant
        H ->>+ MC: Exists(ctx, tenantID, userID)
        MC ->>+ Core: GET /api/v1/internal/tenants/:id/members/:user_id/exists
        Core -->>- MC: {"active": true|false, "tenant_membership_id"?}
        alt Core unreachable / timeout (300ms budget)
            MC -->>- H: error
            H -->> Client: 503 core_unavailable (fail closed — no row written, TAC-FAIL-1)
        else active: false
            MC -->>- H: false, nil
            H -->> Client: 422 grantee_not_active_member
        else active: true
            MC -->>- H: true, membershipID, nil
            H ->>+ DB: INSERT tender_acl_entries (WithTenantTx)
            DB -->>- H: row, or 409 duplicate_grant on uq_tae_active_entry
            H ->> Cache: DEL tac:acl:{tenant}:{tender}:{user} (best-effort, post-commit)
            H -->> Client: 201
        end
    else TAC-3 Revoke
        H ->>+ DB: UPDATE ... SET deleted_at=now() WHERE ... AND deleted_at IS NULL<br/>AND record_version=$4 (WithTenantTx)
        Note over DB: Caller must send its last-read record_version in the request<br/>body (LLD §11.3/§12.1). Zero rows affected (stale version, already<br/>revoked, or no such row) -> 409 optimistic_lock_conflict — see<br/>IMPLEMENTATION_GAP_ANALYSIS.md's Discrepancy 1 for this check's history.
        DB -->>- H: rows affected, or 409 optimistic_lock_conflict
        H ->> Cache: DEL tac:acl:{tenant}:{tender}:{user} (best-effort, post-commit)
        H -->> Client: 204
    else TAC-1 List
        H ->>+ DB: SELECT ... WHERE tenant_id=$1 AND tender_id=$2 (WithTenantTx, not cached)
        DB -->>- H: rows
        H -->> Client: 200
    else TAC-4 CheckAccess
        H ->>+ Cache: GET tac:acl:{tenant}:{tender}:{user}
        alt cache hit
            Cache -->>- H: cached {has_access, access_level, expires_at}
        else cache miss
            Cache -->>- H: miss
            H ->>+ DB: SELECT ... WHERE deleted_at IS NULL AND (expires_at IS NULL OR expires_at > now())
            DB -->>- H: row or none
            H ->> Cache: SET tac:acl:{tenant}:{tender}:{user}, TTL 30s
        end
        H -->> Client: 200 {has_access, access_level, expires_at} — never 404
    end

    H -->>- MW: response (200/201/204/403/422/503)
    MW -->>- GW: return
    Note over MW: gincommon's http_requests_total / _duration_seconds recorded<br/>OTel span ended · structured log line written
    GW -->>- Client: HTTP response
```
> Source: [`docs/architecture/mermaid/request-flow.mmd`](docs/architecture/mermaid/request-flow.mmd)

## Event consumer flow

```mermaid
sequenceDiagram
    participant SQS as tenant-lifecycle-tenderacl-q
    participant C as Consumer (internal/adapter/inbound/consumer)
    participant Idem as processed_events
    participant SVC as internal/core/service
    participant DB as PostgreSQL
    participant DLQ as tenant-lifecycle-tenderacl-q-dlq

    Note over SQS,DLQ: This service is receive-only on the event bus (TAC-EVT-1, TAC-D5).<br/>It publishes zero events — no outbox, no SNS producer. It now consumes<br/>TWO event types on two independent queues (ADR-0007 Wave 3 Phase 3<br/>added a second one, see member-removal-flow.mmd) — this diagram shows<br/>TenantOffboarded only.

    SQS ->>+ C: TenantOffboarded{event_id, tenant_id, occurred_at}
    C ->> C: json.Unmarshal + validate event_type/event_id/tenant_id present

    C ->>+ Idem: IsProcessed(ctx, event_id)
    Idem -->>- C: true | false

    alt already processed (duplicate redelivery)
        C -->> SQS: nil (ack — message deleted, no cascade re-run)
    else not yet processed
        C ->>+ SVC: CascadeDeleteForTenant(ctx, tenant_id)
        SVC ->>+ DB: DELETE FROM tender_acl_entries WHERE tenant_id=$1
        Note over DB: Single-table delete, no multi-repo fan-out (unlike GM-D2's<br/>three mapping tables) — tender_acl_entries is the only<br/>table this service owns. Idempotent by construction: deleting<br/>rows that no longer exist is a no-op, safe to retry in full.
        DB -->>- SVC: rows deleted
        SVC ->> SVC: metrics.RecordCascade(ctx, "success"|"error")
        SVC -->>- C: nil | error

        alt cascade succeeded
            C ->>+ Idem: MarkProcessed(ctx, event_id, "TenantOffboarded", 8d retention)
            Note over Idem: ON CONFLICT DO NOTHING — safe if a concurrent<br/>redelivery already recorded the same event_id
            Idem -->>- C: ok
            C -->> SQS: nil (ack — message deleted)
        else cascade failed
            C -->> SQS: error (message stays on queue for redelivery)
            Note over SQS,DLQ: after maxReceiveCount=5 failed attempts,<br/>SQS moves the message to the DLQ (8-day retention)
        end
    end
    deactivate C

    Note over SQS,DLQ: TAC-EVT-4: a delayed or failed cascade is never a live<br/>authorization risk. A departed tenant's users have no active<br/>membership, so I-8 (AuthZ Enrichment) never issues them headers —<br/>they can never reach TAC-4 regardless of whether stale rows<br/>have been cleaned up yet.
```
> Source: [`docs/architecture/mermaid/event-consumer-flow.mmd`](docs/architecture/mermaid/event-consumer-flow.mmd)

### Per-user-removal cascade flow (ADR-0007 Wave 3 Phase 3)

Structurally identical to the tenant-offboarding flow above — independent queue, independent DLQ,
independent `processed_events` consumer scope, independent metric — with a soft-delete instead of
a hard delete, and one extra field (`subject`, the removed user's id) read from the envelope:

```mermaid
sequenceDiagram
    participant SQS as member-removal-tenderacl-q
    participant C as MemberRemovalConsumer (internal/adapter/inbound/consumer)
    participant Idem as processed_events (consumer=member_removal)
    participant SVC as internal/adapter/outbound/postgres repository
    participant DB as PostgreSQL
    participant DLQ as member-removal-tenderacl-q-dlq

    SQS ->>+ C: TenantMembershipRemoved{id, tenant_id, subject, time}
    C ->> C: validate event type == TenantMembershipRemoved, parse id/tenant_id/subject

    C ->>+ Idem: IsProcessed(ctx, id)
    Idem -->>- C: true | false

    alt already processed (duplicate redelivery)
        C -->> SQS: nil (ack — message deleted, no cascade re-run)
    else not yet processed
        C ->>+ SVC: SoftDeleteForUser(ctx, tenant_id, subject)
        SVC ->>+ DB: UPDATE tender_acl_entries SET deleted_at=now()<br/>WHERE tenant_id=$1 AND user_id=$2 AND deleted_at IS NULL
        Note over DB: SOFT delete, unlike CascadeDeleteForTenant's hard delete —<br/>this table's retention framing (LLD §18) keeps revoked rows for<br/>audit outside of tenant offboarding, the only hard-delete point.<br/>Idempotent by construction: re-running against already-deleted<br/>rows matches zero rows, no error.
        DB -->>- SVC: rows soft-deleted
        SVC -->>- C: deleted count | error
        C ->> C: metrics.RecordMemberRemovalCascade(ctx, "success"|"error")

        alt cascade succeeded
            C ->>+ Idem: MarkProcessed(ctx, id)
            Idem -->>- C: ok
            C -->> SQS: nil (ack — message deleted)
        else cascade failed
            C -->> SQS: error (message stays on queue for redelivery)
            Note over SQS,DLQ: after maxReceiveCount=5 failed attempts,<br/>SQS moves the message to the DLQ (8-day retention)
        end
    end
    deactivate C

    Note over SQS,DLQ: Same TAC-EVT-4-style argument as tenant offboarding, at the<br/>single-user grain: a removed user has no active membership, so<br/>I-8 never issues them headers regardless of whether their stale<br/>ACL rows have been cleaned up yet.
```
> Source: [`docs/architecture/mermaid/member-removal-flow.mmd`](docs/architecture/mermaid/member-removal-flow.mmd)

### Schema dependency on the producer

This service has no Glue Schema Registry integration (LLD §10.4 — explicit, documented exemption
for `TenantOffboarded`, extended by this task to `TenantMembershipRemoved`). `api/asyncapi.yaml`'s
`TenantOffboarded` and `TenantMembershipRemoved` messages each carry an
`x-consumer-schema-dependency` block documenting the exact shape each consumer depends on, since no
automated tooling here would otherwise catch a breaking upstream change to `iam-org-membership`'s
publish path. If a producer ever deprecates either event in favor of a replacement, this service
needs a new consumer for it before the producer's retire-after deadline — track any such notice
manually (there is no CI check that surfaces it).

**Open governance gap** (not resolved by this task — see `IMPLEMENTATION_GAP_ANALYSIS.md`
Discrepancy 7 / `EVENT_COMPATIBILITY_REPORT.md`'s "Governance gap" section): `TenantMembershipRemoved`
has not been run through `iam-org-membership`'s `platform-schemagov` pipeline, and that repo's own
`api/asyncapi.yaml` (its real schema source of truth) does not yet document the event. The event
works correctly today — verified end-to-end against real Postgres in both repos' test suites — but
this specific governance step is genuinely incomplete, flagged explicitly rather than silently
skipped.

## Cache strategy

```mermaid
flowchart TD
    subgraph keys["Cache Key Namespace  (Valkey)  —  the ONLY cache in this service (LLD §9)"]
        k1["tac:acl:{tenant}:{tender}:{user}\n→ {has_access, access_level, expires_at}\nTTL: 30s, no jitter"]
    end

    subgraph check["TAC-4 — Internal Authorization Check  (GET /internal/.../acl/:user_id)"]
        A1([mTLS-only caller, no role check]) --> A2{"tac:acl:{tenant}:{tender}:{user}\ncache hit?"}
        A2 -- yes --> A3["Return cached {has_access, access_level, expires_at}\nNever 404, regardless of hit/miss"]
        A2 -- no  --> A4["SELECT tender_acl_entries\nWHERE deleted_at IS NULL AND\n(expires_at IS NULL OR expires_at > now())"]
        A4 --> A5["SET tac:acl:{tenant}:{tender}:{user}, TTL 30s"]
        A5 --> A6["Return {has_access, access_level, expires_at}"]
    end

    subgraph list["TAC-1 — List  (GET .../acl, admin-gated)"]
        L1([role-checked caller]) --> L2["Always reads live from Postgres\nNOT cached — low-frequency, admin-gated read"]
    end

    subgraph write["Write / Invalidation Path  (TAC-2 Grant, TAC-3 Revoke)"]
        W1([Grant or Revoke, single-row WithTenantTx]) --> W2{Committed?}
        W2 -- no → rollback --> W3[No cache operation\ntransaction/membership-check error returned]
        W2 -- yes --> W4["DEL tac:acl:{tenant}:{tender}:{user}\nAFTER commit — deliberately DELETE not\nUPDATE, so a mid-write process death\nnever serves a stale write-through value"]
        W4 --> W5["Failed DEL is logged, not fatal (TAC-FAIL-2)\nNext TAC-4 read past the 30s TTL\nre-populates from the source of truth regardless"]
    end

    subgraph offboard["Tenant Offboarding Cascade"]
        O1([TenantOffboarded consumed]) --> O2["DELETE FROM tender_acl_entries\nWHERE tenant_id=$1"]
        O2 --> O3["No cache invalidation performed here —\nany cached tac:acl:* entries for the\noffboarded tenant simply expire within 30s"]
    end

    note1["The 30s TTL is the entire staleness budget in this service.\nLLD §17.4 end-to-end chain: grant -> TAC-4 has_access:true (cached)\n-> revoke -> TAC-4 MAY still return the pre-revoke cached value until\nthe DEL lands or the 30s TTL expires, whichever is first — the DEL\nis synchronous with the revoke commit, so in practice this window is\nthe time between commit and the DEL call, not the full 30s."]

    note2["Cache failure mode (TAC-FAIL-2): Valkey down -> TAC-4 falls\nthrough to Postgres on every call (degraded latency, not outage).\n/readyz reports cache degraded but the pod stays in service — writes\nproceed normally, a failed DEL is logged and swallowed."]
```
> Source: [`docs/architecture/mermaid/cache-strategy.mmd`](docs/architecture/mermaid/cache-strategy.mmd)

## Row-Level Security (RLS) and GUC injection

```mermaid
sequenceDiagram
    participant Caller as internal/core/service
    participant TX as pgcommon.WithTenantTx
    participant Pool as pgx Pool (platform-pgcommon)
    participant DB as PostgreSQL (RLS)

    Note over Caller,TX: Every tenant-scoped call on tender_acl_entries goes through<br/>WithTenantTx — there is no code path that hands out a bare<br/>connection/tx, including TAC-4's SELECT-only path.

    Caller ->>+ TX: WithTenantTx(ctx, pool, tenantID, fn)
    TX ->>+ Pool: Begin(ctx)
    Pool ->>+ DB: BEGIN
    DB -->>- Pool: tx started

    TX ->>+ DB: SELECT set_config('app.tenant_id', $1, true)
    Note over DB: third argument true = "is_local" — the GUC is bound for the<br/>lifetime of THIS transaction only. It is unset automatically at<br/>COMMIT/ROLLBACK, so a pooled connection (PgBouncer transaction<br/>mode) can never leak one tenant's GUC into the next tenant's<br/>transaction on the same physical connection.
    DB -->>- TX: ok

    TX ->>+ DB: fn(ctx, tx) — the actual SELECT/INSERT/UPDATE
    Note over DB: RLS POLICY USING/WITH CHECK<br/>(tenant_id = NULLIF(current_setting('app.tenant_id', true), '')::uuid)<br/>enforces tenant isolation at the DB layer — no cross-tenant<br/>leakage is possible even if application code omits a<br/>WHERE tenant_id = ? clause. TAC-4 runs this under the TARGET<br/>tenant's GUC, not the caller's — internal routes are not RLS-exempt.

    Note over DB: Fail-closed on a missing/never-bound GUC: NULLIF(...,'') normalizes<br/>a never-set OR empty-string GUC to NULL, so tenant_id = NULL evaluates<br/>false — zero rows on read, every write rejected. Never an error, never<br/>a leak, even under PgBouncer transaction-pooling connection reuse<br/>(this hardening is already applied in this service's migration,<br/>0001_tender_acl_schema.up.sql — proven necessary by iam-group-mapping's<br/>TestRLS_NoGUCLeakageAcrossPooledConnection, which this service's own<br/>test/rls suite mirrors).

    DB -->>- TX: rows / rows affected
    TX ->>+ DB: COMMIT (or ROLLBACK if fn returned an error)
    DB -->>- TX: done
    TX -->>- Caller: result, error

    Note over Caller,DB: TAC-D8: unlike the source iam-org-membership table, this policy<br/>does NOT wrap tenant_id checks in a rls_check_tenant() SECURITY<br/>DEFINER helper with sampled rls_violation_log forensic logging —<br/>that machinery was deliberately not carried over (mirrors GM-D8).<br/>RLS enforcement itself is unchanged — only the audit-trail-on-<br/>violation behavior was dropped.
```
> Source: [`docs/architecture/mermaid/rls-guc-flow.mmd`](docs/architecture/mermaid/rls-guc-flow.mmd)
> (Note: corrected here to reflect that the `NULLIF(...,'')` hardening is already implemented in
> this service's migration, not a future TODO — the source `.mmd` file's phrasing was written
> conditionally before the migration's final form was confirmed.)

## Observability

- **Metrics** (Prometheus, LLD §14.2):
  - Generic per-request HTTP metrics are platform-gincommon's own —
    `http_requests_total{method,route,status_class,error_class}`,
    `http_request_duration_seconds{method,route,status_class,error_class}` — passed through
    as-is (ObservabilityMiddlewares' MetricsMiddleware) rather than duplicated under a
    `tender_acl_*` name. This service is not yet deployed, so this is the only naming it has
    ever shipped with — see CHANGELOG.md.
  - Business-level metrics keep the `tender_acl_*` prefix, since gincommon has no equivalent:
    `tender_acl_writes_total{op,result}`,
    `tender_acl_grant_checks_total{status}` (`active`/`not_active`/`unavailable`),
    `tender_acl_check_calls_total{status}` (`has_access`/`no_access`),
    `tender_acl_cache_hits_total{key}` / `tender_acl_cache_misses_total{key}`,
    `tender_acl_tenant_offboarding_cascade_total{result}`,
    `tender_acl_member_removal_cascade_total{result}`.
  - Both SQS consumers' own metrics are platform-events' own — `events_consumed_total{queue,
    event_type,status}`, `events_consume_duration_seconds{queue,event_type}`, and, notably,
    `sqs_receive_errors_total{queue}`/`sqs_delete_errors_total{queue}`/
    `sqs_visibility_extension_errors_total{queue}` (the library's own docs flag the latter two as
    *causing* duplicate delivery when non-zero — exactly the failure mode `processed_events`
    exists to survive). Activated by a single `events.InitWithRegisterer("tender-acl",
    buildVersion, gincommon.MetricsRegisterer())` call in `main.go` — the SQS consumer internals
    already compute these on every message regardless; they no-op until this call runs once.
- **Tracing**: OTel Go SDK, W3C Trace Context. Span shape:
  `otelgin (inbound.http) → service.ACLService.<method> → outbound.postgres` (+
  `outbound.membershipcheck` only on Grant, + `tac:*` cache read/DEL spans on
  CheckAccess/Grant/Revoke).
- **Logging**: structured `slog` JSON, carries `trace_id`/`tenant_id` on writes and cascade
  operations. Cache/DB best-effort failures (cache invalidation, cache populate) are logged at
  `WARN`, never escalated to a caller-visible error.
- **Dashboards**: "Tender ACL" Grafana folder — Requests & Writes; Grant-Time Membership Check;
  Tenant-Offboarding Cleanup (LLD §14.4).
- **Alerts** (LLD §14.5): `/readyz` failing >5min → SEV-2; TAC-4 error rate >10%/5min → SEV-2; Core
  (membershipcheck) unreachable during TAC-2 sustained >15min → SEV-3; DLQ depth>0 → SEV-3.

## Concurrency and failure domains

- **TAC-2/TAC-3 writes**: single-row `WithTenantTx`, no cross-row/cross-table consistency
  concern — this service owns exactly one domain table.
- **TAC-2's one synchronous outbound call** (`port.MembershipCheckClient.Exists`) fails **closed**:
  any network error, timeout, or non-2xx response blocks the grant (`503 core_unavailable`), never
  defaults to allowing it (TAC-FAIL-1).
- **TAC-1/TAC-3/TAC-4 have zero synchronous cross-service dependency** — only TAC-2 does
  (TAC-FAIL-3).
- **Own-DB/cache down**: `503 dependency_unavailable` on any route that needs Postgres; Valkey down
  degrades TAC-4 to direct-Postgres reads (higher latency, not an outage) — never an incorrect
  answer served past the 30s cache TTL (TAC-FAIL-2).
- **Tenant-offboarding cascade**: idempotent by construction (repeat `DELETE WHERE tenant_id=$1` is
  a safe no-op); a delayed or even indefinitely-failed cascade is never a live authorization risk
  (TAC-EVT-4) — see the event-consumer-flow diagram's closing note.

## Key invariants

**Decision register (TAC-D1–D9, `tender-acl-service-lld.md` §23):**

| # | Decision |
|---|---|
| TAC-D1 | **Superseded.** Originally a deliberately minimal, near-literal-lift flat package layout — because explicitly interim. Later reversed to adopt the same Clean Architecture layering every sibling service uses (see "Layer model" above and `CHANGELOG.md`) — the interim/disposable framing itself is unchanged, only the package structure. |
| TAC-D2 | Lost composite FK replaced by a synchronous grant-time-only membership check, behind a swappable interface for cheap Wave-4 repoint/delete. |
| TAC-D3 | TAC-1/TAC-4 need no membership join, no cross-service call — only TAC-2 does. |
| TAC-D4 | FK loss is low-risk because the active-grant predicate (TAE-3) was never conditioned on live membership status; AuthZ Enrichment's I-8 gate provides read-time safety independently. |
| TAC-D5 | No new event introduced for grant/revoke — preserves the source's "no bus event" posture. |
| TAC-D6 | Wave-4 entry criteria written down now, per ADR-0007 Action Item 7 (LLD §22). |
| TAC-D7 | Second FK loss (`fk_tae_tenant ON DELETE CASCADE`) replaced by an async SQS subscription, not a second synchronous check — mirrors Wave-2's GM-D2. |
| TAC-D8 | No `rls_check_tenant()`/`rls_violation_log` forensic-logging wrapper — deliberate scope reduction, mirrors Wave-2's GM-D8. |
| TAC-D9 | This v2.0 LLD revision adopts the canonical section template and repoints broken cross-references from earlier drafts; all requirement IDs preserved, only relocated. |

**Event invariants (TAC-EVT-1–5)** and **operational/failure invariants (TAC-FAIL-1–3)**: see
`EVENT_COMPATIBILITY_REPORT.md` and the Concurrency/Failure section above, respectively — both
verified against the actually-built code, not just restated from the LLD.

## Threat model (brief)

- **Cross-tenant data access**: primary defense is RLS (fail-closed on missing GUC); secondary
  defense-in-depth is the handler-layer `requireSameTenant` check comparing the gateway-injected
  tenant against the path parameter. Both must independently fail for a cross-tenant leak to occur.
- **Grant-time authorization bypass**: mitigated by failing closed on any membershipcheck error
  (never fail-open) — an attacker cannot force a grant to succeed by causing `iam-org-membership`
  to become unreachable.
- **TAC-4 is mesh-only**: no public ingress, no JWT/role check — the entire trust boundary is
  network-layer (mTLS + NetworkPolicy), same posture as every other IAM internal route in this
  platform.
- **Incidental PII in `reason`**: free-text admin field, not validated for PII content, treated as
  opaque — same posture as similar free-text fields elsewhere in this platform. Removed only via
  tenant-offboarding cascade or explicit revoke, not a dedicated GDPR delete path.

## What this service deliberately does not have

| Missing (vs. sibling services) | Why |
|---|---|
| A reconcile algorithm (unlike `iam-group-mapping`'s full-replacement diff) | TAC-2/TAC-3 are simple single-row grant/revoke operations, not a bulk-replace resource. |
| An outbound event publisher / outbox table | TAC-EVT-1/TAC-D5 — no ACL event exists in the platform's catalogue and this extraction adds none. |
| A shared audit-log table | No such mechanism exists anywhere in this platform yet (see `IMPLEMENTATION_GAP_ANALYSIS.md`-style note in the sibling services' own gap analyses) — structured logging is the only durable write record today. |
| A GDPR per-user delete path | A departed user's row goes inert (unreachable via TAC-4 once membership lapses, TAC-D4), not deleted, unless the whole tenant offboards. |
| `rls_check_tenant()`/`rls_violation_log` forensic logging | TAC-D8 — deliberately dropped, mirrors GM-D8. RLS enforcement itself is unaffected. |

## Developer tools

`go-arch-lint` (`.go-arch-lint.yml`) enforces the one dependency rule described above.
`golangci-lint` (`.golangci.yml`, invoked via `make lint`) covers everything else. `go vet`/`gofmt`
run in CI (`validate-quality.yml`) alongside both.

## Documentation assets

Mermaid source files backing every diagram in this document live under
`docs/architecture/mermaid/` — see `docs/architecture/README.md` for the full index and rendering
instructions.

## API/event contract doc surfaces

Two dev-tool routes, both gated by `DocsConfig`/`docsAuthMiddleware` the same way (dev-only by
default; opt-in + bearer-token-gated in production via `DOCS_ENABLED`/`DOCS_AUTH_TOKEN`), wired in
`registerDocsRoutes` (`internal/adapter/inbound/http/router.go`):

- **`GET /swagger/*any`** — Swagger UI over the OpenAPI spec generated by `make swag` from handler
  `// @…` annotations (`docs/swagger/`).
- **`GET /asyncapi`** / **`GET /asyncapi.yaml`** — a server-rendered HTML catalog for
  `api/asyncapi.yaml`, and the raw spec itself, both served by
  `internal/adapter/inbound/http/asyncapi.go`. Ported verbatim (renderer logic unchanged) from
  `iam-user-profile`'s identical viewer, which is entirely YAML-driven — it walks
  `components.messages`/`components.schemas` directly rather than a hardcoded name list, so it
  needed no changes to render this service's own two-message, zero-`published`-message spec
  correctly. One adaptation beyond the source spec's own `published`/`consumed` tags (added to
  `api/asyncapi.yaml` so the same tag-driven split applies here): the "Published Messages"
  section/sidebar group is omitted entirely rather than rendered empty, since this service
  publishes zero events (TAC-EVT-1) — `iam-user-profile`'s own viewer always renders both headings
  because it always has at least one published message.
- **The spec is embedded at compile time** (`api/embed.go`, `//go:embed asyncapi.yaml`), not read
  from disk at request time the way `iam-user-profile`'s viewer does — this `Dockerfile`'s final
  stage, like every sibling service's, copies only the compiled binary into the distroless image,
  not the source tree, so a disk-read approach would 404/500 there. Embedding sidesteps the
  problem without a `Dockerfile` change.
