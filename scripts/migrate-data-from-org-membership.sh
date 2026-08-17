#!/usr/bin/env bash
#
# MIGRATION_RUNBOOK.md Phase 2 — one-time export of iam-org-membership's
# current tender_acl_entries rows into this service's database.
#
# This is a plain export-then-load, not a dual-write window (the LLD's
# §21 migration plan chooses this explicitly — see MIGRATION_RUNBOOK.md
# Phase 2's rationale). It is safe to re-run: by default it refuses to
# load into a non-empty target table; pass --force to truncate the target
# first and reload (the runbook's documented rollback path is exactly
# this: truncate and re-run).
#
# Requires: a `psql` client with network access to both databases.
#
# Usage:
#   SOURCE_DSN=postgres://org_membership_app:***@org-membership-host:5432/org_membership \
#   TARGET_DSN=postgres://tender_acl_migrator:***@tender-acl-host:5432/tender_acl \
#     ./scripts/migrate-data-from-org-membership.sh [--force]
#
# TARGET_DSN MUST connect as a role with BYPASSRLS (tender_acl_migrator —
# see internal/adapter/outbound/postgres/migrations/0005_app_role.up.sql), never
# tender_acl_app: this is a bulk, cross-tenant load, and tender_acl_app's
# RLS policy would silently accept 0 rows (or reject cross-tenant INSERTs
# outright) if app.tenant_id isn't set to match every row being loaded.
#
# SOURCE_DSN only needs SELECT on tender_acl_entries — org_membership_app
# or org_membership_migrator both work; the read is a single query, no
# write access is used or required against iam-org-membership.
set -euo pipefail

: "${SOURCE_DSN:?SOURCE_DSN (iam-org-membership, read-only access to tender_acl_entries) must be set}"
: "${TARGET_DSN:?TARGET_DSN (iam-tender-acl, tender_acl_migrator role — BYPASSRLS required) must be set}"

FORCE=0
if [[ "${1:-}" == "--force" ]]; then
  FORCE=1
fi

# Column list is byte-identical between the two schemas (LLD §7.2.1 — the
# table moved unchanged in shape, minus the two FKs that could not survive
# the database split; those FKs carry no columns, so the column set itself
# is unaffected).
COLUMNS="id, tenant_id, tender_id, user_id, tenant_membership_id, access_level, granted_by, reason, expires_at, record_version, created_at, updated_at, deleted_at"

TMPFILE="$(mktemp)"
trap 'rm -f "$TMPFILE"' EXIT

echo "==> Counting source rows..."
SRC_COUNT="$(psql "$SOURCE_DSN" -tA -c "SELECT count(*) FROM tender_acl_entries;")"
echo "    source tender_acl_entries: ${SRC_COUNT} rows"

echo "==> Checking target table state..."
TGT_EXISTING="$(psql "$TARGET_DSN" -tA -c "SELECT count(*) FROM tender_acl_entries;")"
if [[ "${TGT_EXISTING}" != "0" ]]; then
  if [[ "${FORCE}" != "1" ]]; then
    echo "ERROR: target tender_acl_entries already has ${TGT_EXISTING} row(s)." >&2
    echo "       Re-run with --force to truncate the target and reload (documented rollback path)." >&2
    exit 1
  fi
  echo "    --force given: truncating target tender_acl_entries (${TGT_EXISTING} existing row(s))..."
  psql "$TARGET_DSN" -c "TRUNCATE TABLE tender_acl_entries;" >/dev/null
fi

echo "==> Exporting from source..."
psql "$SOURCE_DSN" -c "\copy (SELECT ${COLUMNS} FROM tender_acl_entries ORDER BY id) TO '${TMPFILE}'"

if [[ "${SRC_COUNT}" != "0" ]]; then
  echo "==> Loading into target..."
  psql "$TARGET_DSN" -c "\copy tender_acl_entries (${COLUMNS}) FROM '${TMPFILE}'"
else
  echo "==> Source has 0 rows — nothing to load."
fi

echo "==> Verifying parity (row count + max(updated_at))..."
TGT_COUNT="$(psql "$TARGET_DSN" -tA -c "SELECT count(*) FROM tender_acl_entries;")"
SRC_MAX="$(psql "$SOURCE_DSN" -tA -c "SELECT COALESCE(max(updated_at)::text, '') FROM tender_acl_entries;")"
TGT_MAX="$(psql "$TARGET_DSN" -tA -c "SELECT COALESCE(max(updated_at)::text, '') FROM tender_acl_entries;")"

echo "    source: count=${SRC_COUNT} max(updated_at)=${SRC_MAX:-<none>}"
echo "    target: count=${TGT_COUNT} max(updated_at)=${TGT_MAX:-<none>}"

if [[ "${SRC_COUNT}" != "${TGT_COUNT}" || "${SRC_MAX}" != "${TGT_MAX}" ]]; then
  echo "PARITY CHECK FAILED — do not proceed to Phase 3 until this is resolved." >&2
  exit 1
fi

echo "==> Parity check passed. Phase 2 export complete."
