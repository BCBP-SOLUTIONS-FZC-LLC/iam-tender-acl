-- processed_events is the idempotency ledger for the tenant-lifecycle
-- cleanup consumer (LLD §7.2.2). Not tenant-facing through any API and not
-- subject to Row-Level Security. Composite primary key (event_id,
-- consumer) rather than event_id alone, since a future second consumer
-- (there is none today) would otherwise collide on the same event_id.
CREATE TABLE processed_events (
    event_id     text NOT NULL,
    consumer     text NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (event_id, consumer)
);

-- Backs the 8-day retention cleanup sweep (LLD §7.2.2/§18).
CREATE INDEX idx_processed_events_prune ON processed_events (processed_at);
