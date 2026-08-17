-- touch_row() is installed as a BEFORE UPDATE ... FOR EACH ROW trigger,
-- guarded by "WHEN (OLD.* IS DISTINCT FROM NEW.*)", so it only fires when
-- the row actually changed. Identical to iam-group-mapping's own
-- 0002_touch_row_function.up.sql.
CREATE OR REPLACE FUNCTION touch_row()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    NEW.updated_at := now();
    NEW.record_version := OLD.record_version + 1;
    RETURN NEW;
END;
$$;
