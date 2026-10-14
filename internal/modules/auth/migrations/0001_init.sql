-- The auth module owns the "auth" schema. No other module writes to it, and
-- the minibase_anon / minibase_authenticated roles cannot read its tables.
CREATE SCHEMA IF NOT EXISTS auth;

CREATE TABLE auth.users (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    email           text        NOT NULL CHECK (email = lower(email) AND char_length(email) <= 254),
    password_hash   text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    last_sign_in_at timestamptz
);
CREATE UNIQUE INDEX users_email_key ON auth.users (email);

-- One row per signed-in device. Revoking it kills all its refresh tokens.
CREATE TABLE auth.sessions (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid        NOT NULL REFERENCES auth.users (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    refreshed_at timestamptz,
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz,
    user_agent   text,
    ip           text
);
CREATE INDEX sessions_user_id_idx ON auth.sessions (user_id);

-- Refresh tokens are stored only as SHA-256 hashes. Each is single-use;
-- used_at marks consumption so a replay can be detected.
CREATE TABLE auth.refresh_tokens (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    session_id uuid        NOT NULL REFERENCES auth.sessions (id) ON DELETE CASCADE,
    token_hash bytea       NOT NULL UNIQUE,
    parent_id  bigint      REFERENCES auth.refresh_tokens (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    used_at    timestamptz
);
CREATE INDEX refresh_tokens_session_id_idx ON auth.refresh_tokens (session_id);

-- Helpers for row-level security policies, e.g.
--   CREATE POLICY own_rows ON public.todos USING (user_id = auth.uid());
-- They read the claims the database module sets per request.
CREATE FUNCTION auth.jwt() RETURNS jsonb
    LANGUAGE sql STABLE
    AS $$ SELECT coalesce(nullif(current_setting('request.jwt.claims', true), ''), '{}')::jsonb $$;

CREATE FUNCTION auth.uid() RETURNS uuid
    LANGUAGE sql STABLE
    AS $$ SELECT nullif(auth.jwt() ->> 'sub', '')::uuid $$;

CREATE FUNCTION auth.role() RETURNS text
    LANGUAGE sql STABLE
    AS $$ SELECT auth.jwt() ->> 'role' $$;

-- Request roles may call the helpers but see nothing else in this schema.
REVOKE ALL ON ALL TABLES IN SCHEMA auth FROM PUBLIC;
GRANT USAGE ON SCHEMA auth TO minibase_anon, minibase_authenticated;
GRANT EXECUTE ON FUNCTION auth.jwt(), auth.uid(), auth.role() TO minibase_anon, minibase_authenticated;
