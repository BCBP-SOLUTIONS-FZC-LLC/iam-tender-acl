# Versioning and releases

This repository is a **deployed Go microservice**, not a library other services `go get` — the
`go.mod` module path exists so this repo's own code compiles, not for import by sibling repos
(contrast [`platform-events`](https://github.com/BCBP-SOLUTIONS-FZC-LLC/platform-events)'s
`VERSIONING.md`, which this document is modeled on but differs from for that reason). What gets
versioned here is the **container image**
(`ghcr.io/bcbp-solutions-fzc-llc/iam-tender-acl`) and the **Helm chart** (`deploy/helm/tender-acl/`)
that deploys it, plus the runtime contract the Tender Service, AuthZ Enrichment, the tenant-admin
caller of TAC-1..3, and `iam-org-membership` (the producer of the two events this service consumes,
and the callee of this service's one synchronous outbound call) depend on. Versions are published
with **Git tags** and described in [CHANGELOG.md](./CHANGELOG.md).

This document follows the same SemVer / image-tag / maintainer-process layout as
[`iam-org-membership/VERSIONING.md`](https://github.com/BCBP-SOLUTIONS-FZC-LLC/iam-org-membership/blob/main/VERSIONING.md),
which is itself modeled on `iam-realm-provisioner/VERSIONING.md`.

## Semantic versioning (SemVer)

We use [SemVer 2.0.0](https://semver.org/): `MAJOR.MINOR.PATCH` (e.g. `v1.2.3`).

| Bump | When you change | Examples |
|------|-----------------|----------|
| **MAJOR** | Breaking change in the runtime contract (§ below) | Removing/renaming a TAC-* route, a consumed event `type` name this service dispatches on, the `processed_events.consumer` value for either cascade, a required env var, a domain error **code**, or an incompatible `values.yaml` restructure |
| **MINOR** | New backward-compatible capability | A new optional env var / Helm value, a new `tender_acl_*` metric, an additive request/response field (e.g. TAC-1's `?limit=`/`?offset=` pagination, TAC-D13), a new optional query parameter |
| **PATCH** | Backward-compatible fix | Bug fix (e.g. the `membershipcheck` client's fail-closed contract enforcement, TAC-D11), performance improvement (e.g. a missing index), dependency bump with no observable behavior change, documentation-only correction |

### What counts as the runtime contract

This service has no Go package for another service to import — its "public API" is the wire/deployment
contract every caller and operator depends on. The LLD's own Decision Register
(`docs/lld/iam-lld-tender-acl-service.md` §26, TAC-D1–D13) and Appendix — Error Taxonomy (§20) are
the closest thing to a dedicated frozen-name registry; treat both as frozen in the same spirit,
alongside the tables below.

| In scope (SemVer applies) | Out of scope (may change without MAJOR) |
|---------------------------|------------------------------------------|
| The 4 active routes across two prefixes — `/api/v1/*` (TAC-1 List, TAC-2 Grant, TAC-3 Revoke; admin-gated) and no-prefix `/internal/*` (TAC-4; mesh-only) — method, path, and documented request/response field names (LLD §8, README.md) | `internal/*` package structure, exported Go identifiers, file layout — nothing here is imported by another module |
| The 2 consumed event `type` names and their queue bindings — `TenantMembershipsPurged` on `tenant-lifecycle-tenderacl-q`, `MembershipRevoked` on `member-removal-tenderacl-q` (`api/asyncapi.yaml`) | This service produces **zero** events (TAC-EVT-1/TAC-D5) — there is no produced-event contract to freeze. Envelope field order / JSON key sort on the consume side; which producer call site within `iam-org-membership` emits an event, as long as the `type` name and payload shape are unchanged |
| `processed_events.consumer` values — `tenant_lifecycle_cleanup` and `member_removal` (two literals, one per independent cascade) | Internal dedup-check implementation details (`port.TxRunner`/`withPool`) |
| Domain error **codes** (`internal/core/domain/errors.go`, 12 sentinels — `invalid_request`, `unauthorized`, `insufficient_role`, `invalid_access_level`, `invalid_reason`, `invalid_expiry`, `grantee_not_active_member`, `optimistic_lock_conflict`, `duplicate_grant`, `core_unavailable`, `dependency_unavailable`, `internal_server_error`) and their HTTP statuses (`respondACLError`, LLD §20) | Exact error `message` wording |
| Required env var **names and semantics** (`.env.example`; genuinely required outside dev: `DATABASE_URL`, `MEMBER_REMOVAL_SQS_QUEUE_URL`, `SQS_QUEUE_URL`) | Env var **defaults** (`MEMBERSHIP_CHECK_TIMEOUT_MS=300`, `CORE_INTERNAL_BASE_URL`, `VALKEY_ADDR`, `MIGRATION_DATABASE_URL`'s fallback to `DATABASE_URL`) — tunable without a MAJOR bump unless the new default itself breaks a documented invariant |
| `deploy/helm/tender-acl/values.yaml` top-level key names and shapes consumers actually set (`image.*`, `env`, `envFromSecret`, `autoscaling`, `service.*`, `networkPolicy.*`, replica/HPA fields) | Chart internals (`_helpers.tpl`, template structure) not exposed as a `values.yaml` key |
| Prometheus metric **names** (`internal/adapter/outbound/metrics/metrics.go`) — 9 counters: `tender_acl_writes_total`, `tender_acl_grant_checks_total`, `tender_acl_check_calls_total`, `tender_acl_cache_hits_total`, `tender_acl_cache_misses_total`, `tender_acl_tenant_offboarding_cascade_total`, `tender_acl_member_removal_cascade_total`, `tender_acl_unexpected_event_type_total`, `tender_acl_processed_events_duplicates_total` — dashboards and alert rules key off these exact names | Metric **label cardinality** beyond what's documented; passthrough `http_*` / `events_*` / `pgcommon_*` names owned by `platform-gincommon` / `platform-events` / `platform-pgcommon`, not this repo |
| `GET /healthz` / `/readyz` (HTTP) — existence and meaning (`/readyz` checks the app Postgres pool and Valkey) | `GET /swagger/*any`, `GET /asyncapi`, `GET /asyncapi.yaml` content shape — documentation surfaces, not a contract a caller must hold stable. `METRICS_PORT`'s default value is a Helm default, not a SemVer surface |

TAC-1..4 are this service's **only** endpoint IDs — there is no retired-ID list to carry forward the
way `iam-org-membership`'s own `VERSIONING.md` does. TAC-1..4 are themselves the *successors* to
`iam-org-membership`'s now-retired `P-21`/`P-22`/`P-23`/`I-12` (ADR-0007 Wave 3) — that retirement is
recorded in `iam-org-membership`'s own `VERSIONING.md`/`CHANGELOG.md`, not here.

This service has no Keycloak Admin API client (or any equivalent third-party admin-API dependency)
to track in a compatibility row — Keycloak Admin API access is exclusively Realm Provisioner's
responsibility. This service's one outbound HTTP client (`membershipcheck`, calling
`iam-org-membership`'s internal membership-existence endpoint, TAC-2 grant-time only) is an internal
platform-service dependency, tracked by that repo's own releases, not by a client-library version
pinned here.

### Guarantees

- **Pre-`v1.0.0` (current status):** per [SemVer §4](https://semver.org/#spec-item-4), anything may
  change at any time while the major version is `0` — this service has never been deployed to any
  environment (per [CHANGELOG.md](CHANGELOG.md)'s own statement) and has not yet committed to a
  stable *service* contract via a Git tag. The table above still indicates what's *more* disruptive
  than what within `v0.x`, but a downstream caller should not yet assume `v0.x` compatibility across
  a MINOR bump the way it could once `v1.0.0` ships.
- **MAJOR (once `v1` ships):** we avoid breaking changes to the runtime contract within `v1.x`. A
  breaking change ships as `v2.0.0` with migration notes in the CHANGELOG.
- **MINOR:** safe to redeploy without changing caller URLs, event names, or Helm values, unless you
  opt into a new capability.
- **PATCH:** drop-in image replacement; upgrade recommended for security fixes (every image is
  CVE-scanned, see below).

## Supported releases

| Version | Status | Image tag | Notes |
|---------|--------|-----------|-------|
| *(none tagged)* | **Unreleased** | — | [CHANGELOG.md](CHANGELOG.md) lives entirely under `[Unreleased]`. Helm chart `version` is `0.1.0`, but `appVersion` is currently `"1.0.0"` — an inconsistent pairing worth fixing before the first tag: `appVersion` implies a stable release already shipped, which contradicts this service never having been deployed. Bump both to match whatever the first real tag actually is (e.g. both `0.1.0` for `v0.1.0`), not left as-is. Do not treat the current `appVersion` as a published release | No live deployment yet, so no support window has started |
| `< v0.1.0` | — | — | No tagged Git releases |

**Outstanding items before a first tag is safe to cut:** `CHANGELOG.md`'s "Known gaps" section lists
one open cross-repo item — `MembershipRevoked` has not been run through `iam-org-membership`'s
`platform-schemagov` governance pipeline, and that repo's own `api/asyncapi.yaml` does not yet
document it. This does not block *this* service functioning correctly (verified end-to-end against
real Postgres in both repos' test suites) but is a genuine governance gap, not silently skipped.
Separately, the Chart.yaml `version`/`appVersion` mismatch noted above should be resolved in the
same pass as cutting the first tag.

This service has no formal support-window policy yet, since nothing is running in production against
a tagged release. Once a `v1.0.0` ships to a real environment, this section will define how long a
superseded major line receives security-only fixes (expect the same platform convention
`platform-events`/`iam-realm-provisioner`/`iam-org-membership` use: security fixes only, for a period
the platform team sets, typically ~6 months after the next major).

## Consume a release

This service is **not** consumed via `go get` — do not add this module as a dependency of another Go
service. It is consumed as a **container image** on the mesh (public `/api/v1/*`, internal, no-prefix
`/internal/*`), via the **Helm chart** in `deploy/helm/tender-acl/`. Sibling services call it over
HTTP (the Tender Service's approval workflow and AuthZ Enrichment both call TAC-4).

### Pull and verify the image

Once a tag exists:

```bash
docker pull ghcr.io/bcbp-solutions-fzc-llc/iam-tender-acl:v0.1.0
```

**Today** only `main`-branch images are published (see CI below). Every image pushed from `ci.yml`
is signed keylessly via Sigstore/Cosign (no long-lived key) — verify before deploying:

```bash
# main-branch builds (current — signed by ci.yml's push job)
cosign verify \
  --certificate-identity-regexp "^https://github\.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/.github/workflows/.*@refs/heads/(main|master)$" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/bcbp-solutions-fzc-llc/iam-tender-acl@<digest>
```

Tagged releases, published by `.github/workflows/release.yml`, use `@refs/tags/` in the identity
regexp instead of `@refs/heads/(main|master)`:

```bash
# tagged releases (signed by release.yml's docker job)
cosign verify \
  --certificate-identity-regexp "^https://github\.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/.github/workflows/.*@refs/tags/.*$" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/bcbp-solutions-fzc-llc/iam-tender-acl@<digest>
```

See [README.md § CI](README.md).

### Image tag scheme

`release.yml`'s `docker/metadata-action` step produces, per tag push (same scheme as
`iam-org-membership`/`iam-realm-provisioner`/`iam-authz-enrichment`):

| Tag pattern | Produced for | Example (from `v1.2.3`) |
|-------------|--------------|--------------------------|
| `vMAJOR.MINOR.PATCH` | Every release | `v1.2.3` |
| `vMAJOR.MINOR` | Every release | `v1.2` |
| `vMAJOR` | Every release | `v1` |
| `latest` | Stable releases only (no pre-release suffix) | `latest` |

Separately, `ci.yml` pushes the image on every merge to `main` with git SHA / branch tags — pin
production by a **release tag or digest**, not `latest` or an untagged `main` build.

| Pin style | Use when |
|-----------|----------|
| `vMAJOR.MINOR.PATCH` | Production; exact reproducibility (recommended — also enables digest verification) |
| `vMAJOR.MINOR` | Accept PATCH updates automatically |
| `vMAJOR` | Accept MINOR/PATCH updates automatically — not recommended before `v1.0.0` |
| `latest` / untagged `main` | Local/dev experiments only; never production |

### Deploy via Helm

```bash
helm upgrade tender-acl ./deploy/helm/tender-acl \
  --install \
  --namespace iam \
  --set image.tag=v0.1.0
```

`image.tag` (`deploy/helm/tender-acl/values.yaml`) defaults to the chart's own `appVersion`
(`Chart.yaml`) when unset. Chart `version`/`appVersion` are bumped manually alongside the Git tag
(see the maintainer process below); they are **not** derived automatically from the tag by any
workflow today.

The chart deploys **one** `Deployment` — no `CronJob`s, unlike `iam-org-membership`'s 7. The single
image carries one binary, `/tender-acl`, running the HTTP server (TAC-1..4) and both SQS consumers
(tenant-offboarding + per-user-removal) in-process via one `errgroup` — there is no separate
reconciler/consumer binary to track (`ARCHITECTURE.md` § Deployment).

## Maintainer release process

`.github/workflows/release.yml` implements the validate → build → docker (CVE scan/sign) →
**deploy-gate** → publish pipeline, modeled on `iam-org-membership`'s release workflow — with the
same deliberate property worth knowing before you tag: **the deploy-gate here is not optional.**
`deploy-gate` fails immediately if the `KUBECONFIG_B64` secret isn't set, and the final `publish` job
(the one that creates the GitHub Release) requires `deploy-gate` to have *succeeded* — not merely
run. Practically: **you cannot complete a tagged release of this service today without a real
cluster to deploy to and verify against.** The deploy-gate's own post-deploy health check (a
production-readiness audit hardened this — see `CHANGELOG.md`) now fails loudly rather than silently
passing if `vars.PROMETHEUS_URL` is unset or if Prometheus reports zero scrape targets for this job;
it can be explicitly bypassed via `vars.ALLOW_DEPLOY_WITHOUT_HEALTH_GATE=true`, but that is a
deliberate opt-out, not a default. `ci.yml`'s push-to-`main` path (image to GHCR + Cosign, unversioned
SHA/branch tags) keeps running independently of tagging — it is not replaced by `release.yml`.

When cutting a tagged release:

1. **Merge** all changes for the release to `main`. Confirm the outstanding items in the Supported
   Releases section above are resolved (or no longer apply) before proceeding.

2. **Update `CHANGELOG.md`:** move `[Unreleased]` entries into a new `## [X.Y.Z] - YYYY-MM-DD`
   section. `release.yml`'s `build` job runs `.github/scripts/verify-changelog-entry.sh`, which fails
   the release if this section is missing — cut it *before* tagging.

3. **Bump `deploy/helm/tender-acl/Chart.yaml`'s `version`/`appVersion`** to match `X.Y.Z`, and resolve
   the current `0.1.0`/`1.0.0` mismatch in the same pass (see Supported Releases above). Nothing does
   this automatically — a consumer who deploys the chart without setting `image.tag` explicitly gets
   whatever `appVersion` was last committed, so a forgotten bump here silently ships a stale image.

4. **Run `make ci` locally** to confirm everything is green before tagging:
   ```bash
   make ci   # tidy + fmt-check + vet + lint + test-ci + build
   ```
   Note that `make ci` does **not** include `go-arch-lint` — `go-arch-lint check --project-path .` is
   run separately in `validate-test.yml`. Check its output manually before tagging; a red arch-lint
   run doesn't fail `make ci` by itself.

5. **Create and push an annotated tag** — this is what triggers `release.yml`:
   ```bash
   git tag -a v0.1.0 -m "v0.1.0"
   git push origin v0.1.0
   ```

6. **The release workflow** (`.github/workflows/release.yml`) runs:
   - `validate-test` / `validate-quality` — the same reusable gates `ci.yml` uses, re-run at the
     exact tagged commit. **`validate-test.yml`'s coverage gate defaults to an 85% threshold**
     (`.github/scripts/coverage-gate.sh`) — real measured merged (unit+integration+rls) coverage sits
     around 91.6%, so this gate passes comfortably today, unlike some sibling repos where the
     threshold outruns actual coverage.
   - `build` — verifies the tag matches HEAD (`verify-release-tag.sh`), verifies the CHANGELOG entry
     exists (`verify-changelog-entry.sh`), cross-compiles the single `tender-acl` binary
     (`prepare-release-binary.sh`) for 5 platforms — a convenience for non-Docker runs, not the
     primary distribution artifact.
   - `docker` — builds and pushes the semver-tagged image (see tag scheme above), CVE-scans it (fails
     the release on CRITICAL/HIGH), generates a CycloneDX SBOM and SLSA provenance, signs with Cosign,
     then verifies the signature.
   - `deploy-gate` (requires `KUBECONFIG_B64`; **not skippable** — see above) — live Helm deploy,
     deployed-digest verification against what was pushed, `kubectl rollout status`, then a
     Prometheus-backed error-rate check that auto-rolls-back via `helm rollback` on failure.
   - `publish` — creates the GitHub Release with the CHANGELOG section as notes, plus the binary
     checksums/SBOM/provenance attached. Runs only if `build`, `docker`, **and** `deploy-gate` all
     succeeded.

7. **Notify consumers** — the Tender Service (TAC-1..3 admin surface, TAC-4 approval-workflow
   caller), AuthZ Enrichment (TAC-4 caller), and whoever owns the Helm deployment — with upgrade notes
   if MINOR or MAJOR. Coordinate anything that touches TAC-4's response shape or either consumed
   event's `type` name/queue binding first — those are the highest-blast-radius parts of this
   contract.

### Pre-release tags (optional)

| Tag pattern | Meaning |
|-------------|---------|
| `v1.1.0-rc.1` | Release candidate; not for production unless approved |
| `v1.1.0-beta.1` | Early integration testing |

Both should match a `v[0-9]*.[0-9]*.[0-9]*-*` tag trigger and produce a signed, scanned image — but
never a `latest` tag (see the tag scheme table above).

## Compatibility matrix

| iam-tender-acl | Go (`go.mod`) | Shared platform libraries | `platform-schemagov` (`schema-gov` CLI) |
|---|---|---|---|
| Unreleased (`main`) | `1.26.6` | `platform-gincommon` v1.3.0, `platform-events` v1.4.0, `platform-pgcommon` v1.3.0 | Not applicable — this service is consume-only (TAC-EVT-1/TAC-D5) and owns no produced-schema governance workspace; see `ARCHITECTURE.md` § "What this service deliberately does not have" |

None of the shared libraries is re-exported — a consumer of this *service* never needs them as a
direct dependency of *this* module. Callers depend on the HTTP/event contract, not on any Go package
here. This service has no Keycloak Admin API dependency of its own (see the note above the
Guarantees section) — Keycloak server version is entirely Realm Provisioner's and Keycloak ops'
concern, invisible to this compatibility matrix.

## Related files

| File | Purpose |
|------|---------|
| [CHANGELOG.md](./CHANGELOG.md) | User-facing history per version — currently entirely `[Unreleased]`, including the schemagov governance gap noted above |
| [README.md](./README.md) | Mental model, API/event overview, local dev, CI/CD summary |
| [docs/lld/iam-lld-tender-acl-service.md](./docs/lld/iam-lld-tender-acl-service.md) | The runtime contract in full — §19 open-question register, §20 error taxonomy, §21 integration details, §22 operational considerations |
| [ARCHITECTURE.md](./ARCHITECTURE.md) | Layer model, ports, cache/RLS strategy, deployment topology, key invariants, threat model |
| [.github/workflows/ci.yml](./.github/workflows/ci.yml) | Current image build / Trivy / Cosign-on-`main` pipeline |
| [.github/workflows/release.yml](./.github/workflows/release.yml) | Tag-triggered release pipeline (validate → build → docker → deploy-gate → publish) |
| [deploy/helm/tender-acl/Chart.yaml](./deploy/helm/tender-acl/Chart.yaml) | Helm chart version / app version |
| [go.mod](./go.mod) | Module path and minimum Go version — not a consumable package |
