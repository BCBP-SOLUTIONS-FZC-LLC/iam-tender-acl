REVOKE ALL PRIVILEGES ON
    tender_acl_entries,
    processed_events
FROM tender_acl_app;

REVOKE USAGE ON SCHEMA public FROM tender_acl_app;

DROP ROLE IF EXISTS tender_acl_app;

REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA public FROM tender_acl_migrator;
REVOKE ALL PRIVILEGES ON SCHEMA public FROM tender_acl_migrator;

DROP ROLE IF EXISTS tender_acl_migrator;

DROP TABLE IF EXISTS processed_events;
DROP TABLE IF EXISTS tender_acl_entries;
DROP FUNCTION IF EXISTS touch_row();
DROP TYPE IF EXISTS tender_acl_level;
