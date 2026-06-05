package storagepool

import (
	"database/sql"
	"fmt"
	"time"

	"nas-server/database"
	"nas-server/pkg/idgen"
)

type StoragePool struct {
	ID                    string       `db:"id" json:"id"`
	StorageID             string       `db:"storage_id" json:"storageId"`
	Name                  string       `db:"name" json:"name"`
	Filesystem            string       `db:"filesystem" json:"filesystem"`
	RaidLevel             string       `db:"raid_level" json:"raidLevel"`
	MountPath             string       `db:"mount_path" json:"mountPath"`
	DataPath              string       `db:"data_path" json:"dataPath,omitempty"`
	AutoSnapshotEnabled   bool         `db:"auto_snapshot_enabled" json:"autoSnapshotEnabled"`
	AutoSnapshotSchedule  string       `db:"auto_snapshot_schedule" json:"autoSnapshotSchedule,omitempty"`
	LastAutoSnapshotAt    *time.Time   `db:"last_auto_snapshot_at" json:"lastAutoSnapshotAt,omitempty"`
	NextAutoSnapshotAt    *time.Time   `db:"next_auto_snapshot_at" json:"nextAutoSnapshotAt,omitempty"`
	CacheMode             string       `db:"cache_mode" json:"cacheMode,omitempty"`
	ReadSpeedBytesPerSec  float64      `db:"read_speed_bytes_per_sec" json:"readSpeedBytesPerSec"`
	WriteSpeedBytesPerSec float64      `db:"write_speed_bytes_per_sec" json:"writeSpeedBytesPerSec"`
	SpeedTestedAt         *time.Time   `db:"speed_tested_at" json:"speedTestedAt,omitempty"`
	CreatedAt             time.Time    `db:"created_at" json:"createdAt"`
	UpdatedAt             *time.Time   `db:"updated_at" json:"updatedAt,omitempty"`
	Devices               []PoolDevice `json:"devices"`
}

type PoolDevice struct {
	ID         string `db:"id" json:"id"`
	PoolID     string `db:"pool_id" json:"poolId"`
	DevicePath string `db:"device_path" json:"devicePath"`
	DeviceName string `db:"device_name" json:"deviceName"`
	KernelName string `db:"kernel_name" json:"kernelName"`
	ParentPath string `db:"parent_path" json:"parentPath,omitempty"`
	SizeBytes  uint64 `db:"size_bytes" json:"sizeBytes"`
	SizeHuman  string `db:"size_human" json:"-"`
	Model      string `db:"model" json:"model,omitempty"`
	Serial     string `db:"serial" json:"serial,omitempty"`
	Vendor     string `db:"vendor" json:"vendor,omitempty"`
	Transport  string `db:"transport" json:"transport,omitempty"`
	DeviceRole string `db:"device_role" json:"deviceRole"`
	State      string `db:"-" json:"state"`
	Health     string `db:"-" json:"health"`
}

type SnapshotRecord struct {
	ID               string     `db:"id" json:"id"`
	PoolID           string     `db:"pool_id" json:"poolId"`
	Name             string     `db:"snapshot_name" json:"name"`
	Path             string     `db:"snapshot_path" json:"path"`
	SourcePath       string     `db:"source_path" json:"sourcePath"`
	IsReadOnly       bool       `db:"is_read_only" json:"isReadOnly"`
	Description      string     `db:"description" json:"description,omitempty"`
	CreatedBy        string     `db:"created_by" json:"createdBy,omitempty"`
	SystemSnapshotID uint64     `db:"system_snapshot_id" json:"systemSnapshotId"`
	SystemGeneration uint64     `db:"system_generation" json:"systemGeneration"`
	CreatedAt        time.Time  `db:"created_at" json:"createdAt"`
	UpdatedAt        *time.Time `db:"updated_at" json:"updatedAt,omitempty"`
}

func Add(pool StoragePool, devices []PoolDevice) error {
	tx, err := database.DB.Beginx()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if pool.ID == "" {
		pool.ID = idgen.New()
	}
	if _, err := tx.Exec(
		`INSERT INTO storage_pool (id, storage_id, name, filesystem, raid_level, mount_path, data_path, auto_snapshot_enabled, auto_snapshot_schedule, last_auto_snapshot_at, next_auto_snapshot_at, cache_mode, read_speed_bytes_per_sec, write_speed_bytes_per_sec, speed_tested_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pool.ID, pool.StorageID, pool.Name, pool.Filesystem, pool.RaidLevel, pool.MountPath, pool.DataPath, boolToInt(pool.AutoSnapshotEnabled), pool.AutoSnapshotSchedule, pool.LastAutoSnapshotAt, pool.NextAutoSnapshotAt, pool.CacheMode, pool.ReadSpeedBytesPerSec, pool.WriteSpeedBytesPerSec, pool.SpeedTestedAt, pool.UpdatedAt,
	); err != nil {
		return err
	}

	for _, device := range devices {
		if device.ID == "" {
			device.ID = idgen.New()
		}
		device.PoolID = pool.ID
		if _, err := tx.Exec(
			`INSERT INTO storage_pool_device (id, pool_id, device_path, device_name, kernel_name, parent_path, size_bytes, size_human, model, serial, vendor, transport, device_role) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			device.ID, device.PoolID, device.DevicePath, device.DeviceName, device.KernelName, device.ParentPath, device.SizeBytes, device.SizeHuman, device.Model, device.Serial, device.Vendor, device.Transport, device.DeviceRole,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func List() ([]StoragePool, error) {
	var pools []StoragePool
	if err := database.DB.Select(&pools, `SELECT id, storage_id, name, filesystem, raid_level, mount_path, COALESCE(data_path, '') AS data_path, COALESCE(auto_snapshot_enabled, 0) AS auto_snapshot_enabled, COALESCE(auto_snapshot_schedule, '') AS auto_snapshot_schedule, last_auto_snapshot_at, next_auto_snapshot_at, cache_mode, COALESCE(read_speed_bytes_per_sec, 0) AS read_speed_bytes_per_sec, COALESCE(write_speed_bytes_per_sec, 0) AS write_speed_bytes_per_sec, speed_tested_at, created_at, updated_at FROM storage_pool ORDER BY created_at DESC`); err != nil {
		return nil, err
	}
	for i := range pools {
		devices, err := listDevices(pools[i].ID)
		if err != nil {
			return nil, err
		}
		pools[i].Devices = devices
	}
	return pools, nil
}

func Get(id string) (*StoragePool, error) {
	var pool StoragePool
	if err := database.DB.Get(&pool, `SELECT id, storage_id, name, filesystem, raid_level, mount_path, COALESCE(data_path, '') AS data_path, COALESCE(auto_snapshot_enabled, 0) AS auto_snapshot_enabled, COALESCE(auto_snapshot_schedule, '') AS auto_snapshot_schedule, last_auto_snapshot_at, next_auto_snapshot_at, cache_mode, COALESCE(read_speed_bytes_per_sec, 0) AS read_speed_bytes_per_sec, COALESCE(write_speed_bytes_per_sec, 0) AS write_speed_bytes_per_sec, speed_tested_at, created_at, updated_at FROM storage_pool WHERE id = ?`, id); err != nil {
		return nil, err
	}
	devices, err := listDevices(pool.ID)
	if err != nil {
		return nil, err
	}
	pool.Devices = devices
	return &pool, nil
}

func GetByName(name string) (*StoragePool, error) {
	var pool StoragePool
	if err := database.DB.Get(&pool, `SELECT id, storage_id, name, filesystem, raid_level, mount_path, COALESCE(data_path, '') AS data_path, COALESCE(auto_snapshot_enabled, 0) AS auto_snapshot_enabled, COALESCE(auto_snapshot_schedule, '') AS auto_snapshot_schedule, last_auto_snapshot_at, next_auto_snapshot_at, cache_mode, COALESCE(read_speed_bytes_per_sec, 0) AS read_speed_bytes_per_sec, COALESCE(write_speed_bytes_per_sec, 0) AS write_speed_bytes_per_sec, speed_tested_at, created_at, updated_at FROM storage_pool WHERE name = ?`, name); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	devices, err := listDevices(pool.ID)
	if err != nil {
		return nil, err
	}
	pool.Devices = devices
	return &pool, nil
}

func listDevices(poolID string) ([]PoolDevice, error) {
	var devices []PoolDevice
	err := database.DB.Select(&devices, `SELECT id, pool_id, device_path, device_name, kernel_name, parent_path, size_bytes, size_human, model, serial, vendor, transport, device_role FROM storage_pool_device WHERE pool_id = ? ORDER BY device_role, device_path`, poolID)
	return devices, err
}

func UpdateDevice(device PoolDevice) error {
	_, err := database.DB.Exec(
		`UPDATE storage_pool_device SET device_path = ?, device_name = ?, kernel_name = ?, parent_path = ?, size_bytes = ?, size_human = ?, model = ?, serial = ?, vendor = ?, transport = ?, device_role = ? WHERE id = ? AND pool_id = ?`,
		device.DevicePath,
		device.DeviceName,
		device.KernelName,
		device.ParentPath,
		device.SizeBytes,
		device.SizeHuman,
		device.Model,
		device.Serial,
		device.Vendor,
		device.Transport,
		device.DeviceRole,
		device.ID,
		device.PoolID,
	)
	return err
}

func HasDevice(path string) (bool, error) {
	var count int
	if err := database.DB.Get(&count, `SELECT COUNT(1) FROM storage_pool_device WHERE device_path = ?`, path); err != nil {
		return false, err
	}
	return count > 0, nil
}

func DeleteByID(poolID string) error {
	_, err := database.DB.Exec(`DELETE FROM storage_pool WHERE id = ?`, poolID)
	return err
}

func UpsertCloudPoolRecord(pool StoragePool) error {
	if pool.ID == "" {
		return fmt.Errorf("storage pool id is required")
	}
	now := time.Now()
	_, err := database.DB.Exec(
		`INSERT INTO storage_pool (id, storage_id, name, filesystem, raid_level, mount_path, data_path, auto_snapshot_enabled, auto_snapshot_schedule, last_auto_snapshot_at, next_auto_snapshot_at, cache_mode, read_speed_bytes_per_sec, write_speed_bytes_per_sec, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?)
		 ON CONFLICT(id) DO UPDATE SET
			storage_id = excluded.storage_id,
			name = excluded.name,
			filesystem = excluded.filesystem,
			raid_level = excluded.raid_level,
			mount_path = excluded.mount_path,
			data_path = excluded.data_path,
			auto_snapshot_enabled = excluded.auto_snapshot_enabled,
			auto_snapshot_schedule = excluded.auto_snapshot_schedule,
			last_auto_snapshot_at = excluded.last_auto_snapshot_at,
			next_auto_snapshot_at = excluded.next_auto_snapshot_at,
			cache_mode = excluded.cache_mode,
			updated_at = excluded.updated_at`,
		pool.ID,
		pool.StorageID,
		pool.Name,
		pool.Filesystem,
		pool.RaidLevel,
		pool.MountPath,
		pool.DataPath,
		boolToInt(pool.AutoSnapshotEnabled),
		pool.AutoSnapshotSchedule,
		pool.LastAutoSnapshotAt,
		pool.NextAutoSnapshotAt,
		pool.CacheMode,
		now,
	)
	return err
}

func UpdateAutoSnapshotQueued(poolID string, next time.Time) error {
	now := time.Now()
	_, err := database.DB.Exec(`UPDATE storage_pool SET next_auto_snapshot_at = ?, updated_at = ? WHERE id = ?`, next, now, poolID)
	return err
}

func UpdateAutoSnapshotSuccess(poolID string, last time.Time, next time.Time) error {
	now := time.Now()
	_, err := database.DB.Exec(`UPDATE storage_pool SET last_auto_snapshot_at = ?, next_auto_snapshot_at = ?, updated_at = ? WHERE id = ?`, last, next, now, poolID)
	return err
}

func UpdateBenchmarkResult(poolID string, readSpeedBytesPerSec float64, writeSpeedBytesPerSec float64, testedAt time.Time) error {
	_, err := database.DB.Exec(
		`UPDATE storage_pool SET read_speed_bytes_per_sec = ?, write_speed_bytes_per_sec = ?, speed_tested_at = ?, updated_at = ? WHERE id = ?`,
		readSpeedBytesPerSec,
		writeSpeedBytesPerSec,
		testedAt,
		testedAt,
		poolID,
	)
	return err
}

func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("storage pool name is required")
	}
	return nil
}

func AddSnapshot(snapshot SnapshotRecord) error {
	if snapshot.ID == "" {
		snapshot.ID = idgen.New()
	}
	_, err := database.DB.Exec(
		`INSERT INTO storage_pool_snapshot (id, pool_id, snapshot_name, snapshot_path, source_path, is_read_only, description, created_by, system_snapshot_id, system_generation, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		snapshot.ID, snapshot.PoolID, snapshot.Name, snapshot.Path, snapshot.SourcePath, boolToInt(snapshot.IsReadOnly), snapshot.Description, snapshot.CreatedBy, snapshot.SystemSnapshotID, snapshot.SystemGeneration, snapshot.UpdatedAt,
	)
	return err
}

func ListSnapshotRecords(poolID string) ([]SnapshotRecord, error) {
	var snapshots []SnapshotRecord
	err := database.DB.Select(&snapshots, `SELECT id, pool_id, snapshot_name, snapshot_path, source_path, is_read_only, description, created_by, COALESCE(system_snapshot_id, 0) AS system_snapshot_id, COALESCE(system_generation, 0) AS system_generation, created_at, updated_at FROM storage_pool_snapshot WHERE pool_id = ? ORDER BY created_at DESC`, poolID)
	return snapshots, err
}

func GetSnapshotRecord(poolID string, snapshotID string) (*SnapshotRecord, error) {
	var snapshot SnapshotRecord
	err := database.DB.Get(&snapshot, `SELECT id, pool_id, snapshot_name, snapshot_path, source_path, is_read_only, description, created_by, COALESCE(system_snapshot_id, 0) AS system_snapshot_id, COALESCE(system_generation, 0) AS system_generation, created_at, updated_at FROM storage_pool_snapshot WHERE pool_id = ? AND id = ?`, poolID, snapshotID)
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func DeleteSnapshotRecord(poolID string, snapshotID string) error {
	_, err := database.DB.Exec(`DELETE FROM storage_pool_snapshot WHERE pool_id = ? AND id = ?`, poolID, snapshotID)
	return err
}

func DeleteSnapshotRecordsByPool(poolID string) error {
	_, err := database.DB.Exec(`DELETE FROM storage_pool_snapshot WHERE pool_id = ?`, poolID)
	return err
}

func UpdateSnapshotSystemFields(snapshotID string, systemSnapshotID uint64, systemGeneration uint64) error {
	_, err := database.DB.Exec(
		`UPDATE storage_pool_snapshot SET system_snapshot_id = ?, system_generation = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		systemSnapshotID,
		systemGeneration,
		snapshotID,
	)
	return err
}

func ResetBenchmarkResult(poolID string, updatedAt time.Time) error {
	_, err := database.DB.Exec(
		`UPDATE storage_pool SET read_speed_bytes_per_sec = 0, write_speed_bytes_per_sec = 0, speed_tested_at = NULL, updated_at = ? WHERE id = ?`,
		updatedAt,
		poolID,
	)
	return err
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
