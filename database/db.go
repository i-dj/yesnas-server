package database

import (
	_ "embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

//go:embed init.sql
var initSQL string

var DB *sqlx.DB

func InitDB(dbPath string) error {
	dir := filepath.Dir(dbPath)
	if dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create database directory: %w", err)
		}
	}

	var err error
	DB, err = sqlx.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	if err = DB.Ping(); err != nil {
		return fmt.Errorf("failed to verify database connection: %w", err)
	}

	DB.SetMaxOpenConns(1)
	DB.SetMaxIdleConns(1)

	pragmas := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA foreign_keys=ON;",
	}
	for _, pragma := range pragmas {
		if _, err = DB.Exec(pragma); err != nil {
			log.Printf("warning: failed to execute SQLite pragma: %s err=%v", strings.TrimSpace(pragma), err)
		}
	}

	log.Printf("database initialized: %s", dbPath)
	return nil
}

// CreateTables initializes the database schema from the embedded SQL file.
func CreateTables() error {
	if DB == nil {
		return fmt.Errorf("database is not initialized; call InitDB first")
	}

	_, err := DB.Exec(initSQL)
	if err != nil {
		return fmt.Errorf("failed to initialize database schema: %w", err)
	}

	log.Println("database schema checked/created")
	return nil
}

func EnsureStorageSchema() error {
	return ensureColumns("storage", map[string]string{
		"provider":     "TEXT DEFAULT ''",
		"host":         "TEXT DEFAULT ''",
		"port":         "INTEGER DEFAULT 0",
		"url":          "TEXT DEFAULT ''",
		"password":     "TEXT DEFAULT ''",
		"token":        "TEXT DEFAULT ''",
		"domain":       "TEXT DEFAULT ''",
		"share_name":   "TEXT DEFAULT ''",
		"root_path":    "TEXT DEFAULT ''",
		"extra_config": "TEXT DEFAULT ''",
	})
}

func EnsureStorageTokenSchema() error {
	if DB == nil {
		return fmt.Errorf("database is not initialized; call InitDB first")
	}

	if err := ensureColumns("storage_token", map[string]string{
		"token_type":    "TEXT DEFAULT ''",
		"access_token":  "TEXT DEFAULT ''",
		"refresh_token": "TEXT DEFAULT ''",
		"expiry":        "DATETIME",
		"scope":         "TEXT DEFAULT ''",
		"raw_json":      "TEXT DEFAULT ''",
	}); err != nil {
		return err
	}

	return nil
}

func EnsureOAuthBrokerSchema() error {
	if DB == nil {
		return fmt.Errorf("database is not initialized; call InitDB first")
	}
	if _, err := DB.Exec(`
CREATE TABLE IF NOT EXISTS oauth_broker_connection (
    id                    TEXT PRIMARY KEY,
    broker_base_url       TEXT NOT NULL UNIQUE,
    device_id             TEXT NOT NULL,
    device_name           TEXT DEFAULT '',
    public_key_pem        TEXT NOT NULL,
    private_key_pem       TEXT NOT NULL,
    registered_at         DATETIME,
    last_auth_at          DATETIME,
    created_at            DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at            DATETIME
);
CREATE TABLE IF NOT EXISTS oauth_broker_session (
    session_id            TEXT PRIMARY KEY,
    broker_base_url       TEXT NOT NULL,
    provider              TEXT NOT NULL,
    storage_id            TEXT DEFAULT '',
    name                  TEXT DEFAULT '',
    root_path             TEXT DEFAULT '',
    status                TEXT DEFAULT 'pending',
    authorize_url         TEXT DEFAULT '',
    expires_at            DATETIME,
    created_at            DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at            DATETIME
);
CREATE TABLE IF NOT EXISTS oauth_broker_storage_token (
    id                    TEXT PRIMARY KEY,
    storage_id            TEXT NOT NULL UNIQUE,
    broker_base_url       TEXT NOT NULL,
    provider              TEXT NOT NULL,
    broker_refresh_token  TEXT NOT NULL,
    rclone_token_url      TEXT NOT NULL,
    rclone_config_json    TEXT DEFAULT '',
    created_at            DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at            DATETIME,
    FOREIGN KEY (storage_id) REFERENCES storage(id) ON DELETE CASCADE
);
`); err != nil {
		return fmt.Errorf("failed to create oauth broker tables: %w", err)
	}
	if err := ensureColumns("oauth_broker_connection", map[string]string{
		"device_name":   "TEXT DEFAULT ''",
		"registered_at": "DATETIME",
		"last_auth_at":  "DATETIME",
		"created_at":    "DATETIME DEFAULT CURRENT_TIMESTAMP",
		"updated_at":    "DATETIME",
	}); err != nil {
		return err
	}
	if err := ensureColumns("oauth_broker_session", map[string]string{
		"storage_id":    "TEXT DEFAULT ''",
		"name":          "TEXT DEFAULT ''",
		"root_path":     "TEXT DEFAULT ''",
		"status":        "TEXT DEFAULT 'pending'",
		"authorize_url": "TEXT DEFAULT ''",
		"expires_at":    "DATETIME",
		"created_at":    "DATETIME DEFAULT CURRENT_TIMESTAMP",
		"updated_at":    "DATETIME",
	}); err != nil {
		return err
	}
	if err := ensureColumns("oauth_broker_storage_token", map[string]string{
		"broker_refresh_token": "TEXT NOT NULL DEFAULT ''",
		"rclone_token_url":     "TEXT NOT NULL DEFAULT ''",
		"rclone_config_json":   "TEXT DEFAULT ''",
		"created_at":           "DATETIME DEFAULT CURRENT_TIMESTAMP",
		"updated_at":           "DATETIME",
	}); err != nil {
		return err
	}
	return ensureIndexes([]string{
		`CREATE INDEX IF NOT EXISTS idx_oauth_broker_session_status ON oauth_broker_session(status, expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_oauth_broker_storage_token_storage_id ON oauth_broker_storage_token(storage_id)`,
	})
}

func EnsureStoragePoolSchema() error {
	if DB == nil {
		return fmt.Errorf("database is not initialized; call InitDB first")
	}
	if err := ensureColumns("storage_pool", map[string]string{
		"data_path":                 "TEXT DEFAULT ''",
		"auto_snapshot_enabled":     "INTEGER DEFAULT 0",
		"auto_snapshot_schedule":    "TEXT DEFAULT ''",
		"last_auto_snapshot_at":     "DATETIME",
		"next_auto_snapshot_at":     "DATETIME",
		"read_speed_bytes_per_sec":  "REAL DEFAULT 0",
		"write_speed_bytes_per_sec": "REAL DEFAULT 0",
		"speed_tested_at":           "DATETIME",
	}); err != nil {
		return err
	}
	return nil
}

func EnsureStoragePoolSnapshotSchema() error {
	if DB == nil {
		return fmt.Errorf("database is not initialized; call InitDB first")
	}
	if err := ensureColumns("storage_pool_snapshot", map[string]string{
		"system_snapshot_id": "INTEGER DEFAULT 0",
		"system_generation":  "INTEGER DEFAULT 0",
	}); err != nil {
		return err
	}
	return nil
}

func EnsureJobsSchema() error {
	if DB == nil {
		return fmt.Errorf("database is not initialized; call InitDB first")
	}
	return ensureColumns("jobs", map[string]string{
		"title":         "TEXT DEFAULT ''",
		"storage_id":    "TEXT DEFAULT ''",
		"resource_type": "TEXT DEFAULT ''",
		"resource_id":   "TEXT DEFAULT ''",
		"progress":      "INTEGER DEFAULT 0",
		"message":       "TEXT DEFAULT ''",
		"error_message": "TEXT DEFAULT ''",
		"payload_json":  "TEXT DEFAULT ''",
		"result_json":   "TEXT DEFAULT ''",
		"started_at":    "DATETIME",
		"finished_at":   "DATETIME",
	})
}

func EnsureUsersSchema() error {
	if DB == nil {
		return fmt.Errorf("database is not initialized; call InitDB first")
	}
	if err := ensureColumns("users", map[string]string{
		"display_name":  "TEXT DEFAULT ''",
		"is_admin":      "INTEGER DEFAULT 0",
		"avatar":        "TEXT DEFAULT ''",
		"password_hash": "TEXT DEFAULT ''",
		"status":        "TEXT NOT NULL DEFAULT 'active'",
	}); err != nil {
		return err
	}
	return ensureIndexes([]string{
		`CREATE INDEX IF NOT EXISTS idx_users_status ON users(status)`,
	})
}

func EnsureUserSessionSchema() error {
	if DB == nil {
		return fmt.Errorf("database is not initialized; call InitDB first")
	}
	if err := ensureColumns("user_sessions", map[string]string{
		"status":        "TEXT NOT NULL DEFAULT 'active'",
		"ip_address":    "TEXT DEFAULT ''",
		"user_agent":    "TEXT DEFAULT ''",
		"updated_at":    "DATETIME DEFAULT CURRENT_TIMESTAMP",
		"expires_at":    "DATETIME",
		"last_seen_at":  "DATETIME",
		"logged_out_at": "DATETIME",
	}); err != nil {
		return err
	}
	return ensureIndexes([]string{
		`CREATE INDEX IF NOT EXISTS idx_user_sessions_user_id ON user_sessions(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_user_sessions_status_expires_at ON user_sessions(status, expires_at)`,
	})
}

func EnsureAuditLogSchema() error {
	if DB == nil {
		return fmt.Errorf("database is not initialized; call InitDB first")
	}
	if err := ensureColumns("audit_logs", map[string]string{
		"category":           "TEXT NOT NULL DEFAULT 'user'",
		"severity":           "TEXT NOT NULL DEFAULT 'info'",
		"source":             "TEXT NOT NULL DEFAULT 'api'",
		"event":              "TEXT NOT NULL DEFAULT ''",
		"action":             "TEXT NOT NULL DEFAULT ''",
		"success":            "INTEGER DEFAULT 1",
		"actor_user_id":      "TEXT DEFAULT ''",
		"actor_username":     "TEXT DEFAULT ''",
		"actor_display_name": "TEXT DEFAULT ''",
		"ip_address":         "TEXT DEFAULT ''",
		"ip_type":            "TEXT DEFAULT ''",
		"country_code":       "TEXT DEFAULT ''",
		"country":            "TEXT DEFAULT ''",
		"city":               "TEXT DEFAULT ''",
		"user_agent":         "TEXT DEFAULT ''",
		"method":             "TEXT DEFAULT ''",
		"path":               "TEXT DEFAULT ''",
		"resource_type":      "TEXT DEFAULT ''",
		"resource_id":        "TEXT DEFAULT ''",
		"resource_name":      "TEXT DEFAULT ''",
		"keyword":            "TEXT DEFAULT ''",
		"content":            "TEXT DEFAULT ''",
		"key_data_json":      "TEXT DEFAULT ''",
		"message":            "TEXT DEFAULT ''",
		"details_json":       "TEXT DEFAULT ''",
		"occurred_at":        "DATETIME DEFAULT CURRENT_TIMESTAMP",
	}); err != nil {
		return err
	}
	return ensureIndexes([]string{
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_occurred_at ON audit_logs(occurred_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_category_occurred_at ON audit_logs(category, occurred_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_severity_occurred_at ON audit_logs(severity, occurred_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_source_occurred_at ON audit_logs(source, occurred_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_actor_user_id_occurred_at ON audit_logs(actor_user_id, occurred_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_event_occurred_at ON audit_logs(event, occurred_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_country_city_occurred_at ON audit_logs(country_code, city, occurred_at DESC)`,
	})
}

func EnsureFileShareSchema() error {
	if DB == nil {
		return fmt.Errorf("database is not initialized; call InitDB first")
	}
	if err := ensureColumns("file_shares", map[string]string{
		"storage_pool_id": "TEXT DEFAULT ''",
		"protocols":       "TEXT DEFAULT '[]'",
		"user_ids":        "TEXT DEFAULT '[]'",
		"client_networks": "TEXT DEFAULT '[]'",
		"status":          "TEXT NOT NULL DEFAULT 'enabled'",
	}); err != nil {
		return err
	}
	return ensureIndexes([]string{
		`CREATE INDEX IF NOT EXISTS idx_file_shares_status ON file_shares(status)`,
	})
}

func ensureColumns(table string, columns map[string]string) error {
	if DB == nil {
		return fmt.Errorf("database is not initialized; call InitDB first")
	}

	existing, err := getTableColumns(table)
	if err != nil {
		return err
	}

	for name, ddl := range columns {
		if existing[name] {
			continue
		}
		stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, name, ddl)
		if _, err := DB.Exec(stmt); err != nil {
			return fmt.Errorf("failed to add column %s to table %s: %w", name, table, err)
		}
	}
	return nil
}

func ensureIndexes(statements []string) error {
	if DB == nil {
		return fmt.Errorf("database is not initialized; call InitDB first")
	}
	for _, stmt := range statements {
		if _, err := DB.Exec(stmt); err != nil {
			return fmt.Errorf("failed to create index with statement %q: %w", stmt, err)
		}
	}
	return nil
}

func getTableColumns(table string) (map[string]bool, error) {
	rows, err := DB.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, fmt.Errorf("failed to read table %s schema: %w", table, err)
	}
	defer rows.Close()

	columns := make(map[string]bool)
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal any
			pk         int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk); err != nil {
			return nil, fmt.Errorf("failed to scan table %s schema: %w", table, err)
		}
		columns[strings.ToLower(name)] = true
	}
	return columns, rows.Err()
}

func RunSeed(filePath string) error {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read seed file: %v", err)
	}

	_, err = DB.Exec(string(content))
	if err != nil {
		return fmt.Errorf("failed to execute seed data: %v", err)
	}

	log.Println("seed data initialized")
	return nil
}
