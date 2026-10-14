-- Let API requests reach tables in the public schema. What each request may
-- actually see or change is decided by row-level security policies; the API
-- refuses anon/authenticated access to tables without RLS enabled.
GRANT USAGE ON SCHEMA public TO minibase_anon, minibase_authenticated;

-- Existing objects…
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO minibase_anon, minibase_authenticated;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO minibase_anon, minibase_authenticated;

-- …and objects created later by this same database user (e.g. in psql).
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO minibase_anon, minibase_authenticated;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO minibase_anon, minibase_authenticated;
