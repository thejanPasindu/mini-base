-- The admin module owns the "admin" schema: dashboard operators and their
-- sessions. These accounts are separate from application users (auth.users)
-- and are never reachable through the public APIs.
CREATE SCHEMA IF NOT EXISTS admin;
REVOKE ALL ON SCHEMA admin FROM PUBLIC;

CREATE TABLE admin.accounts (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    email           text        NOT NULL UNIQUE CHECK (email = lower(email) AND char_length(email) <= 254),
    password_hash   text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    last_sign_in_at timestamptz
);

-- Session tokens are random and stored only as SHA-256 hashes.
CREATE TABLE admin.sessions (
    token_hash   bytea       PRIMARY KEY,
    account_id   uuid        NOT NULL REFERENCES admin.accounts (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    ip           text,
    user_agent   text
);
CREATE INDEX sessions_account_id_idx ON admin.sessions (account_id);
CREATE INDEX sessions_expires_at_idx ON admin.sessions (expires_at);
