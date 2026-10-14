-- Database roles used for row-level security.
--
-- Requests with the anon key run as minibase_anon; requests from signed-in
-- users run as minibase_authenticated. Requests with the service key run as
-- the connection user (normally the table owner), which bypasses RLS.
--
-- NOLOGIN: nobody connects as these roles; the server switches to them per
-- transaction with SET LOCAL ROLE. Roles are cluster-wide, hence IF NOT EXISTS.
-- Requires the connection user to have CREATEROLE (or be a superuser).
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'minibase_anon') THEN
        CREATE ROLE minibase_anon NOLOGIN NOINHERIT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'minibase_authenticated') THEN
        CREATE ROLE minibase_authenticated NOLOGIN NOINHERIT;
    END IF;
END
$$;

-- Allow the connection user to SET ROLE to them.
GRANT minibase_anon, minibase_authenticated TO CURRENT_USER;
