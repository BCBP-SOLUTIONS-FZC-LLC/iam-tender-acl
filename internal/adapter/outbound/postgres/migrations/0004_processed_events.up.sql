-- processed_events is the idempotency ledger backing both the
-- tenant-offboarding cascade consumer and the per-user-removal cascade
-- consumer (ADR-0007 Wave 3 Phase 3 added the second one — LLD §7.2.2).
-- Not tenant-facing through any API and not subject to Row-Level Security.
-- Composite primary key (event_id, consumer) rather than event_id alone,
-- so the two consumers dedup independently instead of colliding on the
-- same event_id from an unrelated relay.
--
-- event_id is `uuid`, not `text` — mirroring iam-group-mapping's own
-- processed_events table (migrations/0006_processed_events.up.sql), which
-- documents this as a deliberate improvement over the `text` shape
-- iam-org-membership's original convention used: this service's event ids
-- are always genuine UUIDs (validated via uuid.Parse in both
-- OffboardingConsumer and MemberRemovalConsumer before ever reaching this
-- store), so the stronger column type is strictly more correct here.
--
-- No event_type or expires_at column: retention is enforced by batched-
-- delete pruning against processed_at at cleanup time (8 days, see
-- ProcessedEvents.CleanupExpired), not a precomputed per-row expiry.
CREATE TABLE processed_events (
    event_id     uuid NOT NULL,
    consumer     text NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (event_id, consumer)
);

-- Backs the 8-day retention cleanup sweep (LLD §7.2.2/§18).
CREATE INDEX idx_processed_events_prune ON processed_events (processed_at);
