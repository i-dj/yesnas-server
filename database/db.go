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
			return fmt.Errorf("无法创建数据库目录: %w", err)
		}
	}

	var err error
	DB, err = sqlx.Open("sqlite", dbPath) // ← 两处都改：sqlx.Open + "sqlite3"
	if err != nil {
		return fmt.Errorf("打开数据库失败: %w", err)
	}

	if err = DB.Ping(); err != nil {
		return fmt.Errorf("数据库连接验证失败: %w", err)
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
			log.Printf("警告: 执行 SQLite pragma 失败: %s err=%v", strings.TrimSpace(pragma), err)
		}
	}

	log.Printf("数据库初始化成功: %s", dbPath)
	return nil
}

// CreateTables 执行嵌入的 SQL 文件来初始化表结构
func CreateTables() error {
	if DB == nil {
		return fmt.Errorf("数据库未初始化，请先调用 InitDB")
	}

	_, err := DB.Exec(initSQL)
	if err != nil {
		return fmt.Errorf("初始化表结构失败: %w", err)
	}

	log.Println("数据库表结构检查/创建完成")
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
		return fmt.Errorf("数据库未初始化，请先调用 InitDB")
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
		return fmt.Errorf("数据库未初始化，请先调用 InitDB")
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
		return fmt.Errorf("数据库未初始化，请先调用 InitDB")
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
		return fmt.Errorf("数据库未初始化，请先调用 InitDB")
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
		return fmt.Errorf("数据库未初始化，请先调用 InitDB")
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
			return fmt.Errorf("为表 %s 添加列 %s 失败: %w", table, name, err)
		}
	}
	return nil
}

func getTableColumns(table string) (map[string]bool, error) {
	rows, err := DB.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, fmt.Errorf("读取表 %s 结构失败: %w", table, err)
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
			return nil, fmt.Errorf("扫描表 %s 结构失败: %w", table, err)
		}
		columns[strings.ToLower(name)] = true
	}
	return columns, rows.Err()
}

func RunSeed(filePath string) error {
	// 1. 读取 SQL 文件
	content, err := os.ReadFile(filePath)
	if err != nil {
		// 如果文件不存在，可以选择忽略或者报错
		return fmt.Errorf("读取 seed 文件失败: %v", err)
	}

	// 2. 执行 SQL
	// 注意：Exec 可以执行包含多个语句的字符串
	_, err = DB.Exec(string(content))
	if err != nil {
		return fmt.Errorf("执行 seed 数据失败: %v", err)
	}

	log.Println("✅ Seed 数据初始化成功")
	return nil
}
