-- Audit log of dashboard actions, and saved SQL snippets per admin.

-- Who did what in the dashboard (and with `minibase admin ...`). actor keeps
-- the email even after the account is deleted. Rows older than
-- MINIBASE_ADMIN_AUDIT_RETENTION are deleted hourly. Admins can run any SQL,
-- so this log records actions; it is not tamper-proof against an admin.
CREATE TABLE admin.audit_log (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at         timestamptz NOT NULL DEFAULT now(),
    account_id uuid        REFERENCES admin.accounts (id) ON DELETE SET NULL,
    actor      text        NOT NULL,
    ip         text,
    action     text        NOT NULL,
    target     text        NOT NULL DEFAULT '',
    detail     jsonb       NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_log_at_idx ON admin.audit_log (at);
CREATE INDEX audit_log_action_idx ON admin.audit_log (action, id DESC);
-- SQL editor history is read from here per admin.
CREATE INDEX audit_log_history_idx ON admin.audit_log (account_id, id DESC) WHERE action = 'sql.run';

CREATE TABLE admin.snippets (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid        NOT NULL REFERENCES admin.accounts (id) ON DELETE CASCADE,
    name       text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    query      text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (account_id, name)
);
