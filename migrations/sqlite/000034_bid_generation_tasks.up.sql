CREATE TABLE IF NOT EXISTS bid_generation_tasks (
    id TEXT PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    session_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    status TEXT NOT NULL,
    revision INTEGER NOT NULL DEFAULT 1,
    state TEXT NOT NULL,
    last_error TEXT NOT NULL DEFAULT '',
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_expires_at DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS bid_generation_one_active_session
    ON bid_generation_tasks (tenant_id, session_id)
    WHERE status NOT IN ('completed', 'cancelled');
CREATE INDEX IF NOT EXISTS bid_generation_recovery
    ON bid_generation_tasks (status, lease_expires_at);
CREATE INDEX IF NOT EXISTS bid_generation_owner_session
    ON bid_generation_tasks (tenant_id, user_id, session_id, created_at DESC);
