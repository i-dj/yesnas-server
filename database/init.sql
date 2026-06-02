-- Storage table
CREATE TABLE IF NOT EXISTS storage (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    mount_path   TEXT NOT NULL UNIQUE,
    type         TEXT NOT NULL,
    provider     TEXT DEFAULT '',
    location     TEXT NOT NULL,
    host         TEXT DEFAULT '',
    port         INTEGER DEFAULT 0,
    url          TEXT DEFAULT '',
    username     TEXT DEFAULT '',
    password     TEXT DEFAULT '',
    token        TEXT DEFAULT '',
    domain       TEXT DEFAULT '',
    share_name   TEXT DEFAULT '',
    root_path    TEXT DEFAULT '',
    extra_config TEXT DEFAULT '',
    status       TEXT DEFAULT 'online',
    status_text  TEXT,
    total_size   INTEGER DEFAULT 0,
    free_size    INTEGER DEFAULT 0,
    created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME
);

CREATE TABLE IF NOT EXISTS storage_token (
    id             TEXT PRIMARY KEY,
    storage_id     TEXT NOT NULL UNIQUE,
    token_type     TEXT DEFAULT '',
    access_token   TEXT DEFAULT '',
    refresh_token  TEXT DEFAULT '',
    expiry         DATETIME,
    scope          TEXT DEFAULT '',
    raw_json       TEXT DEFAULT '',
    created_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME,
    FOREIGN KEY (storage_id) REFERENCES storage(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_storage_token_storage_id ON storage_token(storage_id);

CREATE TABLE IF NOT EXISTS storage_pool (
    id                          TEXT PRIMARY KEY,
    storage_id                  TEXT NOT NULL UNIQUE,
    name                        TEXT NOT NULL UNIQUE,
    filesystem                  TEXT NOT NULL,
    raid_level                  TEXT NOT NULL,
    mount_path                  TEXT NOT NULL UNIQUE,
    data_path                   TEXT DEFAULT '',
    cache_mode                  TEXT DEFAULT '',
    read_speed_bytes_per_sec    REAL DEFAULT 0,
    write_speed_bytes_per_sec   REAL DEFAULT 0,
    speed_tested_at             DATETIME,
    created_at                  DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at                  DATETIME,
    FOREIGN KEY (storage_id) REFERENCES storage(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS storage_pool_device (
    id           TEXT PRIMARY KEY,
    pool_id       TEXT NOT NULL,
    device_path   TEXT NOT NULL,
    device_name   TEXT NOT NULL,
    kernel_name   TEXT NOT NULL,
    parent_path   TEXT DEFAULT '',
    size_bytes    INTEGER DEFAULT 0,
    size_human    TEXT DEFAULT '',
    model         TEXT DEFAULT '',
    serial        TEXT DEFAULT '',
    vendor        TEXT DEFAULT '',
    transport     TEXT DEFAULT '',
    device_role   TEXT NOT NULL,
    created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(pool_id, device_path),
    FOREIGN KEY (pool_id) REFERENCES storage_pool(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_storage_pool_device_pool_id ON storage_pool_device(pool_id);

CREATE TABLE IF NOT EXISTS storage_pool_snapshot (
    id                  TEXT PRIMARY KEY,
    pool_id             TEXT NOT NULL,
    snapshot_name       TEXT NOT NULL,
    snapshot_path       TEXT NOT NULL,
    source_path         TEXT NOT NULL,
    is_read_only        INTEGER DEFAULT 1,
    description         TEXT DEFAULT '',
    created_by          TEXT DEFAULT 'system',
    system_snapshot_id  INTEGER DEFAULT 0,
    system_generation   INTEGER DEFAULT 0,
    created_at          DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at          DATETIME,
    UNIQUE(pool_id, snapshot_path),
    FOREIGN KEY (pool_id) REFERENCES storage_pool(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_storage_pool_snapshot_pool_id ON storage_pool_snapshot(pool_id);

CREATE TABLE IF NOT EXISTS jobs (
    id             TEXT PRIMARY KEY,
    type           TEXT NOT NULL,
    status         TEXT NOT NULL,
    title          TEXT DEFAULT '',
    storage_id     TEXT DEFAULT '',
    resource_type  TEXT DEFAULT '',
    resource_id    TEXT DEFAULT '',
    progress       INTEGER DEFAULT 0,
    message        TEXT DEFAULT '',
    error_message  TEXT DEFAULT '',
    payload_json   TEXT DEFAULT '',
    result_json    TEXT DEFAULT '',
    created_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
    started_at     DATETIME,
    finished_at    DATETIME
);

CREATE INDEX IF NOT EXISTS idx_jobs_status_created_at ON jobs(status, created_at);
CREATE INDEX IF NOT EXISTS idx_jobs_type_created_at ON jobs(type, created_at);

CREATE TABLE IF NOT EXISTS users (
    id             TEXT PRIMARY KEY,
    username       TEXT NOT NULL UNIQUE,
    display_name   TEXT DEFAULT '',
    is_admin       INTEGER DEFAULT 0,
    avatar         TEXT DEFAULT '',
    password_hash  TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'active',
    created_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME
);

CREATE INDEX IF NOT EXISTS idx_users_status ON users(status);

CREATE TABLE IF NOT EXISTS file_shares (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    storage_pool_id TEXT DEFAULT '',
    path            TEXT NOT NULL,
    protocols       TEXT DEFAULT '[]',
    user_ids        TEXT DEFAULT '[]',
    client_networks TEXT DEFAULT '[]',
    status          TEXT NOT NULL DEFAULT 'enabled',
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME
);

CREATE INDEX IF NOT EXISTS idx_file_shares_status ON file_shares(status);

-- Favorites table
CREATE TABLE IF NOT EXISTS favorites (
    id           TEXT PRIMARY KEY,
    storage_id   TEXT NOT NULL,
    file_name    TEXT NOT NULL,
    file_path    TEXT NOT NULL,
    color        TEXT,
    created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    -- Relative paths must be unique within the same storage.
    UNIQUE(storage_id, file_path),
    -- Cascade delete favorites when the storage is deleted.
    FOREIGN KEY (storage_id) REFERENCES storage(id) ON DELETE CASCADE
);

-- Index optimization
CREATE INDEX IF NOT EXISTS idx_fav_storage ON favorites(storage_id);

-- Trash table
CREATE TABLE IF NOT EXISTS recycle_bin (
    id             TEXT PRIMARY KEY,
    storage_id     TEXT NOT NULL,
    file_name      TEXT NOT NULL,
    original_path  TEXT NOT NULL,
    recycle_path   TEXT NOT NULL,
    file_type      TEXT NOT NULL,
    size           INTEGER DEFAULT 0,
    deleted_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
    expires_at     DATETIME,
    UNIQUE(storage_id, original_path),
    FOREIGN KEY (storage_id) REFERENCES storage(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_recycle_storage ON recycle_bin(storage_id);
CREATE INDEX IF NOT EXISTS idx_recycle_deleted_at ON recycle_bin(deleted_at);
