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

func EnsureStoragePoolSchema() error {
	if DB == nil {
		return fmt.Errorf("database is not initialized; call InitDB first")
	}
	if err := ensureColumns("storage_pool", map[string]string{
		"data_path":                 "TEXT DEFAULT ''",
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
