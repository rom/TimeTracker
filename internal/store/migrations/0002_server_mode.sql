-- 0002_server_mode.sql - authentication, sessions and project membership.
--
-- Layer 2 of docs/MVP_PLAN.md. Everything here is inert in local mode: a
-- single-user installation has one user row with no password and no sessions,
-- and the tables simply stay empty. That is deliberate - the same database file
-- can be moved from a laptop to a server without a conversion step.

-- Credentials on the existing user row. All are nullable, because a user may
-- authenticate by password, by OIDC, or (in local mode) not at all.
ALTER TABLE users ADD COLUMN password_hash TEXT NOT NULL DEFAULT '';
-- The immutable OIDC subject claim. Accounts are linked by this and never by
-- email, which is mutable and can be reassigned in a directory - the classic
-- account-takeover route. See docs/adr/0006-authentication-model.md.
ALTER TABLE users ADD COLUMN oidc_subject TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN oidc_issuer  TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN totp_secret  TEXT NOT NULL DEFAULT '';
-- Set when the password was last changed, so a forced rotation can be detected.
ALTER TABLE users ADD COLUMN password_set_at TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN last_login_at   TEXT NOT NULL DEFAULT '';

-- A subject is unique per issuer, and only among rows that actually have one.
CREATE UNIQUE INDEX idx_users_oidc ON users(oidc_issuer, oidc_subject)
    WHERE oidc_subject != '';

-- Sessions are server-side records referenced by an opaque cookie. Not JWTs:
-- a server-side session can be revoked instantly, which is the property that
-- matters when someone leaves. See docs/adr/0006-authentication-model.md.
CREATE TABLE sessions (
    -- The SHA-256 of the cookie value, never the value itself. A stolen
    -- database therefore does not yield usable session cookies.
    id_hash     TEXT PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at  TEXT    NOT NULL,
    -- Both lifetimes are enforced: idle expiry catches an abandoned browser,
    -- absolute expiry bounds a session that is kept artificially alive.
    last_seen_at TEXT   NOT NULL,
    expires_at   TEXT   NOT NULL,
    ip           TEXT   NOT NULL DEFAULT '',
    user_agent   TEXT   NOT NULL DEFAULT '',
    -- The CSRF token bound to this session. Stored rather than derived so that
    -- rotating the session id also rotates the token.
    csrf_token  TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_sessions_user    ON sessions(user_id);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);

-- Project membership is the scoping dimension of the role model: a manager's
-- authority is real but bounded by which projects they are attached to.
-- See docs/adr/0008-rbac-model.md.
CREATE TABLE project_members (
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id)    ON DELETE CASCADE,
    -- Optional per-project role override, e.g. a member who manages one project.
    role_override TEXT NOT NULL DEFAULT '',
    -- Per-person rate on this project: the level between the assignment and the
    -- project in the resolution order described in docs/DESIGN.md.
    rate_minor INTEGER NOT NULL DEFAULT 0,
    created_at TEXT    NOT NULL,
    PRIMARY KEY (project_id, user_id)
);

CREATE INDEX idx_project_members_user ON project_members(user_id);

-- A client user is attached to exactly one customer, and sees nothing else.
ALTER TABLE users ADD COLUMN client_customer_id INTEGER NOT NULL DEFAULT 0;
