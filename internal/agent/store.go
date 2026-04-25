package agent

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mordilloSan/go-monitoring/internal/model/container"
	modelnet "github.com/mordilloSan/go-monitoring/internal/model/network"
	procmodel "github.com/mordilloSan/go-monitoring/internal/model/process"
	"github.com/mordilloSan/go-monitoring/internal/model/smart"
	"github.com/mordilloSan/go-monitoring/internal/model/system"
	"github.com/mordilloSan/go-monitoring/internal/model/systemd"
	"github.com/mordilloSan/go-monitoring/internal/version"
	_ "modernc.org/sqlite"
)

const storeSchemaVersion = 3

const (
	resolution1m   = "1m"
	resolution10m  = "10m"
	resolution20m  = "20m"
	resolution120m = "120m"
	resolution480m = "480m"
)

var historyRetention = map[string]time.Duration{
	resolution1m:   time.Hour,
	resolution10m:  12 * time.Hour,
	resolution20m:  24 * time.Hour,
	resolution120m: 7 * 24 * time.Hour,
	resolution480m: 30 * 24 * time.Hour,
}

type rollupStep struct {
	shorter        string
	longer         string
	window         time.Duration
	minShorterRows int
}

var rollupSteps = []rollupStep{
	{shorter: resolution1m, longer: resolution10m, window: 10 * time.Minute, minShorterRows: 9},
	{shorter: resolution10m, longer: resolution20m, window: 20 * time.Minute, minShorterRows: 2},
	{shorter: resolution20m, longer: resolution120m, window: 120 * time.Minute, minShorterRows: 6},
	{shorter: resolution120m, longer: resolution480m, window: 480 * time.Minute, minShorterRows: 4},
}

type Store struct {
	db   *sql.DB
	path string
}

type SummaryResponse struct {
	CapturedAt int64 `json:"captured_at"`
	system.CombinedData
}

type HistoryItem[T any] struct {
	CapturedAt int64 `json:"captured_at"`
	Stats      T     `json:"stats"`
}

type HistoryResponse[T any] struct {
	Resolution string           `json:"resolution"`
	Items      []HistoryItem[T] `json:"items"`
}

type CurrentItemsResponse[T any] struct {
	CapturedAt int64 `json:"captured_at"`
	Items      []T   `json:"items"`
}

type CurrentDataResponse[T any] struct {
	CapturedAt int64 `json:"captured_at"`
	Data       T     `json:"data"`
}

type ContainerCurrent struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	Image       string                 `json:"image,omitempty"`
	Ports       string                 `json:"ports,omitempty"`
	Status      string                 `json:"status,omitempty"`
	Health      container.DockerHealth `json:"health,omitempty"`
	Cpu         float64                `json:"cpu_percent"`
	Mem         float64                `json:"memory_mb"`
	NetworkSent float64                `json:"network_sent_mb,omitempty,omitzero"`
	NetworkRecv float64                `json:"network_recv_mb,omitempty,omitzero"`
	Bandwidth   [2]uint64              `json:"bandwidth_bytes,omitempty,omitzero"`
}

type SmartDeviceCurrent struct {
	ID   string          `json:"id"`
	Key  string          `json:"key"`
	Data smart.SmartData `json:"data"`
}

type snapshotJSONPayload struct {
	infoJSON             string
	statsJSON            string
	containerHistoryJSON string
	detailsJSON          any
}

type MetaResponse struct {
	Version              string            `json:"version"`
	DataDir              string            `json:"data_dir"`
	DBPath               string            `json:"db_path"`
	ListenAddr           string            `json:"listen_addr"`
	CollectorInterval    string            `json:"collector_interval"`
	SmartRefreshInterval string            `json:"smart_refresh_interval"`
	Retention            map[string]string `json:"retention"`
}

func OpenStore(dataDir string) (*Store, error) {
	dbPath := filepath.Join(dataDir, "metrics.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	store := &Store{
		db:   db,
		path: dbPath,
	}
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) init() error {
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
	}
	for _, pragma := range pragmas {
		if _, err := s.db.Exec(pragma); err != nil {
			return err
		}
	}

	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	switch version {
	case 0:
		if err := s.migrateV1(); err != nil {
			return err
		}
	case 1:
		if err := s.migrateV2(); err != nil {
			return err
		}
		return s.migrateV3()
	case 2:
		return s.migrateV3()
	case storeSchemaVersion:
		return nil
	default:
		return fmt.Errorf("unsupported store schema version %d", version)
	}
	return nil
}

func (s *Store) migrateV1() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS system_current (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			captured_at INTEGER NOT NULL,
			info_json TEXT NOT NULL,
			stats_json TEXT NOT NULL,
			details_json TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS system_stats_history (
			resolution TEXT NOT NULL,
			captured_at INTEGER NOT NULL,
			stats_json TEXT NOT NULL,
			PRIMARY KEY (resolution, captured_at)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_system_stats_history_captured_at
			ON system_stats_history (captured_at)`,
		`CREATE TABLE IF NOT EXISTS container_stats_history (
			resolution TEXT NOT NULL,
			captured_at INTEGER NOT NULL,
			stats_json TEXT NOT NULL,
			PRIMARY KEY (resolution, captured_at)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_container_stats_history_captured_at
			ON container_stats_history (captured_at)`,
		`CREATE TABLE IF NOT EXISTS containers_current (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			updated INTEGER NOT NULL,
			data_json TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_containers_current_name
			ON containers_current (name)`,
		`CREATE TABLE IF NOT EXISTS systemd_services_current (
			name TEXT PRIMARY KEY,
			updated INTEGER NOT NULL,
			data_json TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS smart_devices_current (
			id TEXT PRIMARY KEY,
			device_key TEXT NOT NULL,
			updated INTEGER NOT NULL,
			data_json TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_smart_devices_current_key
			ON smart_devices_current (device_key)`,
		`CREATE TABLE IF NOT EXISTS processes_current (
			pid INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			cpu_percent REAL NOT NULL,
			memory_percent REAL NOT NULL,
			updated INTEGER NOT NULL,
			data_json TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_processes_current_cpu
			ON processes_current (cpu_percent DESC, memory_percent DESC)`,
		`CREATE TABLE IF NOT EXISTS process_count_current (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			updated INTEGER NOT NULL,
			data_json TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS programs_current (
			name TEXT PRIMARY KEY,
			cpu_percent REAL NOT NULL,
			memory_percent REAL NOT NULL,
			updated INTEGER NOT NULL,
			data_json TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_programs_current_cpu
			ON programs_current (cpu_percent DESC, memory_percent DESC)`,
		`CREATE TABLE IF NOT EXISTS connections_current (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			updated INTEGER NOT NULL,
			data_json TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS irq_current (
			irq TEXT PRIMARY KEY,
			total INTEGER NOT NULL,
			updated INTEGER NOT NULL,
			data_json TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_irq_current_total
			ON irq_current (total DESC)`,
		fmt.Sprintf("PRAGMA user_version = %d", storeSchemaVersion),
	}
	for _, stmt := range statements {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) migrateV2() error {
	statements := []string{
		"DELETE FROM containers_current",
		"DELETE FROM container_stats_history",
		"PRAGMA user_version = 2",
	}
	for _, stmt := range statements {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) migrateV3() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS processes_current (
			pid INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			cpu_percent REAL NOT NULL,
			memory_percent REAL NOT NULL,
			updated INTEGER NOT NULL,
			data_json TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_processes_current_cpu
			ON processes_current (cpu_percent DESC, memory_percent DESC)`,
		`CREATE TABLE IF NOT EXISTS process_count_current (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			updated INTEGER NOT NULL,
			data_json TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS programs_current (
			name TEXT PRIMARY KEY,
			cpu_percent REAL NOT NULL,
			memory_percent REAL NOT NULL,
			updated INTEGER NOT NULL,
			data_json TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_programs_current_cpu
			ON programs_current (cpu_percent DESC, memory_percent DESC)`,
		`CREATE TABLE IF NOT EXISTS connections_current (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			updated INTEGER NOT NULL,
			data_json TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS irq_current (
			irq TEXT PRIMARY KEY,
			total INTEGER NOT NULL,
			updated INTEGER NOT NULL,
			data_json TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_irq_current_total
			ON irq_current (total DESC)`,
		fmt.Sprintf("PRAGMA user_version = %d", storeSchemaVersion),
	}
	for _, stmt := range statements {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) WriteSnapshot(capturedAt int64, data *system.CombinedData) (err error) {
	if data == nil {
		return errors.New("snapshot data is nil")
	}

	payload, err := buildSnapshotJSONPayload(data)
	if err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if err = writeSnapshotRows(tx, capturedAt, payload); err != nil {
		return err
	}
	if err = replaceCurrentSnapshotTables(tx, capturedAt, data); err != nil {
		return err
	}
	if err = upsertMeta(tx, "last_persisted_at", strconv.FormatInt(capturedAt, 10)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func buildSnapshotJSONPayload(data *system.CombinedData) (snapshotJSONPayload, error) {
	var payload snapshotJSONPayload
	var err error

	payload.infoJSON, err = marshalJSON(data.Info)
	if err != nil {
		return payload, err
	}
	payload.statsJSON, err = marshalJSON(data.Stats)
	if err != nil {
		return payload, err
	}
	payload.containerHistoryJSON, err = marshalJSON(data.Containers)
	if err != nil {
		return payload, err
	}
	if data.Details != nil {
		payload.detailsJSON, err = marshalJSON(data.Details)
	}
	return payload, err
}

func writeSnapshotRows(tx *sql.Tx, capturedAt int64, payload snapshotJSONPayload) error {
	var err error
	if _, err = tx.Exec(`
		INSERT INTO system_current (singleton, captured_at, info_json, stats_json, details_json)
		VALUES (1, ?, ?, ?, ?)
		ON CONFLICT(singleton) DO UPDATE SET
			captured_at = excluded.captured_at,
			info_json = excluded.info_json,
			stats_json = excluded.stats_json,
			details_json = COALESCE(excluded.details_json, system_current.details_json)
	`, capturedAt, payload.infoJSON, payload.statsJSON, payload.detailsJSON); err != nil {
		return err
	}

	if _, err = tx.Exec(`
		INSERT INTO system_stats_history (resolution, captured_at, stats_json)
		VALUES (?, ?, ?)
	`, resolution1m, capturedAt, payload.statsJSON); err != nil {
		return err
	}

	if _, err = tx.Exec(`
		INSERT INTO container_stats_history (resolution, captured_at, stats_json)
		VALUES (?, ?, ?)
	`, resolution1m, capturedAt, payload.containerHistoryJSON); err != nil {
		return err
	}
	return nil
}

func replaceCurrentSnapshotTables(tx *sql.Tx, capturedAt int64, data *system.CombinedData) error {
	if err := replaceCurrentContainers(tx, capturedAt, data.Containers); err != nil {
		return err
	}
	if err := replaceCurrentSystemd(tx, capturedAt, data.SystemdServices); err != nil {
		return err
	}
	if err := replaceCurrentProcessCount(tx, capturedAt, data.ProcessCount); err != nil {
		return err
	}
	if err := replaceCurrentProcesses(tx, capturedAt, data.Processes); err != nil {
		return err
	}
	if err := replaceCurrentPrograms(tx, capturedAt, data.Programs); err != nil {
		return err
	}
	if err := replaceCurrentConnections(tx, capturedAt, data.Connections); err != nil {
		return err
	}
	return replaceCurrentIRQ(tx, capturedAt, data.IRQs)
}

func replaceCurrentContainers(tx *sql.Tx, capturedAt int64, items []*container.Stats) error {
	if _, err := tx.Exec("DELETE FROM containers_current"); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	stmt, err := tx.Prepare("INSERT INTO containers_current (id, name, updated, data_json) VALUES (?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, item := range items {
		if item == nil {
			continue
		}
		current := ContainerCurrent{
			ID:          item.Id,
			Name:        item.Name,
			Image:       item.Image,
			Ports:       item.Ports,
			Status:      item.Status,
			Health:      item.Health,
			Cpu:         item.Cpu,
			Mem:         item.Mem,
			NetworkSent: item.NetworkSent,
			NetworkRecv: item.NetworkRecv,
			Bandwidth:   item.Bandwidth,
		}
		raw, err := marshalJSON(current)
		if err != nil {
			return err
		}
		if _, err := stmt.Exec(current.ID, current.Name, capturedAt, raw); err != nil {
			return err
		}
	}
	return nil
}

func replaceCurrentSystemd(tx *sql.Tx, capturedAt int64, items []*systemd.Service) error {
	if _, err := tx.Exec("DELETE FROM systemd_services_current"); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	stmt, err := tx.Prepare("INSERT INTO systemd_services_current (name, updated, data_json) VALUES (?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, item := range items {
		if item == nil {
			continue
		}
		raw, err := marshalJSON(item)
		if err != nil {
			return err
		}
		if _, err := stmt.Exec(item.Name, capturedAt, raw); err != nil {
			return err
		}
	}
	return nil
}

func replaceCurrentProcessCount(tx *sql.Tx, capturedAt int64, item *procmodel.Count) error {
	if _, err := tx.Exec("DELETE FROM process_count_current"); err != nil {
		return err
	}
	if item == nil {
		return nil
	}
	raw, err := marshalJSON(item)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO process_count_current (singleton, updated, data_json) VALUES (1, ?, ?)", capturedAt, raw)
	return err
}

func replaceCurrentProcesses(tx *sql.Tx, capturedAt int64, items []procmodel.Process) error {
	if _, err := tx.Exec("DELETE FROM processes_current"); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	stmt, err := tx.Prepare("INSERT INTO processes_current (pid, name, cpu_percent, memory_percent, updated, data_json) VALUES (?, ?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, item := range items {
		raw, err := marshalJSON(item)
		if err != nil {
			return err
		}
		if _, err := stmt.Exec(item.PID, item.Name, item.CPUPercent, item.MemoryPercent, capturedAt, raw); err != nil {
			return err
		}
	}
	return nil
}

func replaceCurrentPrograms(tx *sql.Tx, capturedAt int64, items []procmodel.Program) error {
	if _, err := tx.Exec("DELETE FROM programs_current"); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	stmt, err := tx.Prepare("INSERT INTO programs_current (name, cpu_percent, memory_percent, updated, data_json) VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, item := range items {
		raw, err := marshalJSON(item)
		if err != nil {
			return err
		}
		if _, err := stmt.Exec(item.Name, item.CPUPercent, item.MemoryPercent, capturedAt, raw); err != nil {
			return err
		}
	}
	return nil
}

func replaceCurrentConnections(tx *sql.Tx, capturedAt int64, item *modelnet.ConnectionStats) error {
	if _, err := tx.Exec("DELETE FROM connections_current"); err != nil {
		return err
	}
	if item == nil {
		return nil
	}
	raw, err := marshalJSON(item)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO connections_current (singleton, updated, data_json) VALUES (1, ?, ?)", capturedAt, raw)
	return err
}

func replaceCurrentIRQ(tx *sql.Tx, capturedAt int64, items []modelnet.IRQStat) error {
	if _, err := tx.Exec("DELETE FROM irq_current"); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	stmt, err := tx.Prepare("INSERT INTO irq_current (irq, total, updated, data_json) VALUES (?, ?, ?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, item := range items {
		raw, err := marshalJSON(item)
		if err != nil {
			return err
		}
		if _, err := stmt.Exec(item.IRQ, item.Total, capturedAt, raw); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) WriteSmartDevices(capturedAt int64, items map[string]smart.SmartData) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.Exec("DELETE FROM smart_devices_current"); err != nil {
		return err
	}

	if len(items) > 0 {
		stmt, prepErr := tx.Prepare(`
			INSERT INTO smart_devices_current (id, device_key, updated, data_json)
			VALUES (?, ?, ?, ?)
		`)
		if prepErr != nil {
			return prepErr
		}
		defer stmt.Close()

		for key, item := range items {
			raw, marshalErr := marshalJSON(item)
			if marshalErr != nil {
				return marshalErr
			}
			id := key
			if item.DiskName != "" {
				id = item.DiskName
			}
			if _, execErr := stmt.Exec(id, key, capturedAt, raw); execErr != nil {
				return execErr
			}
		}
	}

	if err = upsertMeta(tx, "last_smart_refresh_at", strconv.FormatInt(capturedAt, 10)); err != nil {
		return err
	}

	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func upsertMeta(tx *sql.Tx, key, value string) error {
	_, err := tx.Exec(`
		INSERT INTO meta (key, value)
		VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, key, value)
	return err
}

func (s *Store) Summary() (*SummaryResponse, error) {
	var (
		capturedAt int64
		infoJSON   string
		statsJSON  string
		detailsRaw sql.NullString
	)
	err := s.db.QueryRow(`
		SELECT captured_at, info_json, stats_json, details_json
		FROM system_current
		WHERE singleton = 1
	`).Scan(&capturedAt, &infoJSON, &statsJSON, &detailsRaw)
	if err != nil {
		return nil, err
	}

	var summary SummaryResponse
	summary.CapturedAt = capturedAt
	if unmarshalErr := json.Unmarshal([]byte(infoJSON), &summary.Info); unmarshalErr != nil {
		return nil, unmarshalErr
	}
	if unmarshalErr := json.Unmarshal([]byte(statsJSON), &summary.Stats); unmarshalErr != nil {
		return nil, unmarshalErr
	}
	if detailsRaw.Valid && detailsRaw.String != "" {
		summary.Details = &system.Details{}
		if unmarshalErr := json.Unmarshal([]byte(detailsRaw.String), summary.Details); unmarshalErr != nil {
			return nil, unmarshalErr
		}
	}
	if summary.Containers, err = s.currentContainerStats(); err != nil {
		return nil, err
	}
	if summary.SystemdServices, err = s.CurrentSystemdItems(); err != nil {
		return nil, err
	}
	return &summary, nil
}

func (s *Store) currentContainerStats() ([]*container.Stats, error) {
	rows, err := s.db.Query("SELECT data_json FROM containers_current ORDER BY name ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []*container.Stats{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var current ContainerCurrent
		if err := json.Unmarshal([]byte(raw), &current); err != nil {
			return nil, err
		}
		items = append(items, &container.Stats{
			Id:          current.ID,
			Name:        current.Name,
			Image:       current.Image,
			Ports:       current.Ports,
			Status:      current.Status,
			Health:      current.Health,
			Cpu:         current.Cpu,
			Mem:         current.Mem,
			NetworkSent: current.NetworkSent,
			NetworkRecv: current.NetworkRecv,
			Bandwidth:   current.Bandwidth,
		})
	}
	return items, rows.Err()
}

func (s *Store) CurrentContainers() (*CurrentItemsResponse[ContainerCurrent], error) {
	capturedAt, err := s.currentCapturedAt()
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query("SELECT data_json FROM containers_current ORDER BY name ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	resp := &CurrentItemsResponse[ContainerCurrent]{CapturedAt: capturedAt, Items: []ContainerCurrent{}}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var current ContainerCurrent
		if err := json.Unmarshal([]byte(raw), &current); err != nil {
			return nil, err
		}
		resp.Items = append(resp.Items, current)
	}
	return resp, rows.Err()
}

func (s *Store) CurrentSystemd() (*CurrentItemsResponse[*systemd.Service], error) {
	capturedAt, err := s.currentCapturedAt()
	if err != nil {
		return nil, err
	}
	items, err := s.CurrentSystemdItems()
	if err != nil {
		return nil, err
	}
	return &CurrentItemsResponse[*systemd.Service]{
		CapturedAt: capturedAt,
		Items:      items,
	}, nil
}

func (s *Store) CurrentSystemdItems() ([]*systemd.Service, error) {
	rows, err := s.db.Query("SELECT data_json FROM systemd_services_current ORDER BY name ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []*systemd.Service{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var current systemd.Service
		if err := json.Unmarshal([]byte(raw), &current); err != nil {
			return nil, err
		}
		items = append(items, &current)
	}
	return items, rows.Err()
}

func (s *Store) CurrentProcessCount() (*CurrentDataResponse[procmodel.Count], error) {
	return currentSingleton[procmodel.Count](s, "process_count_current")
}

func (s *Store) CurrentProcesses() (*CurrentItemsResponse[procmodel.Process], error) {
	capturedAt, err := s.currentCapturedAt()
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query("SELECT data_json FROM processes_current ORDER BY cpu_percent DESC, memory_percent DESC, pid ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	resp := &CurrentItemsResponse[procmodel.Process]{CapturedAt: capturedAt, Items: []procmodel.Process{}}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var current procmodel.Process
		if err := json.Unmarshal([]byte(raw), &current); err != nil {
			return nil, err
		}
		resp.Items = append(resp.Items, current)
	}
	return resp, rows.Err()
}

func (s *Store) CurrentPrograms() (*CurrentItemsResponse[procmodel.Program], error) {
	capturedAt, err := s.currentCapturedAt()
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query("SELECT data_json FROM programs_current ORDER BY cpu_percent DESC, memory_percent DESC, name ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	resp := &CurrentItemsResponse[procmodel.Program]{CapturedAt: capturedAt, Items: []procmodel.Program{}}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var current procmodel.Program
		if err := json.Unmarshal([]byte(raw), &current); err != nil {
			return nil, err
		}
		resp.Items = append(resp.Items, current)
	}
	return resp, rows.Err()
}

func (s *Store) CurrentConnections() (*CurrentDataResponse[modelnet.ConnectionStats], error) {
	return currentSingleton[modelnet.ConnectionStats](s, "connections_current")
}

func (s *Store) CurrentIRQ() (*CurrentItemsResponse[modelnet.IRQStat], error) {
	capturedAt, err := s.currentCapturedAt()
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query("SELECT data_json FROM irq_current ORDER BY total DESC, irq ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	resp := &CurrentItemsResponse[modelnet.IRQStat]{CapturedAt: capturedAt, Items: []modelnet.IRQStat{}}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var current modelnet.IRQStat
		if err := json.Unmarshal([]byte(raw), &current); err != nil {
			return nil, err
		}
		resp.Items = append(resp.Items, current)
	}
	return resp, rows.Err()
}

func (s *Store) CurrentSmartDevices() (*CurrentItemsResponse[SmartDeviceCurrent], error) {
	var capturedAt int64
	if err := s.db.QueryRow(`
		SELECT value FROM meta WHERE key = 'last_smart_refresh_at'
	`).Scan(&capturedAt); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	rows, err := s.db.Query(`
		SELECT id, device_key, data_json
		FROM smart_devices_current
		ORDER BY device_key ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	resp := &CurrentItemsResponse[SmartDeviceCurrent]{CapturedAt: capturedAt, Items: []SmartDeviceCurrent{}}
	for rows.Next() {
		var (
			id  string
			key string
			raw string
		)
		if err := rows.Scan(&id, &key, &raw); err != nil {
			return nil, err
		}
		item := SmartDeviceCurrent{ID: id, Key: key}
		if err := json.Unmarshal([]byte(raw), &item.Data); err != nil {
			return nil, err
		}
		resp.Items = append(resp.Items, item)
	}
	return resp, rows.Err()
}

func (s *Store) SystemHistory(resolution string, from, to int64, limit int) (*HistoryResponse[system.Stats], error) {
	items := []HistoryItem[system.Stats]{}
	rows, err := s.db.Query(`
		SELECT captured_at, stats_json
		FROM system_stats_history
		WHERE resolution = ? AND captured_at >= ? AND captured_at <= ?
		ORDER BY captured_at ASC
		LIMIT ?
	`, resolution, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			capturedAt int64
			raw        string
		)
		if err := rows.Scan(&capturedAt, &raw); err != nil {
			return nil, err
		}
		var stats system.Stats
		if err := json.Unmarshal([]byte(raw), &stats); err != nil {
			return nil, err
		}
		items = append(items, HistoryItem[system.Stats]{
			CapturedAt: capturedAt,
			Stats:      stats,
		})
	}
	return &HistoryResponse[system.Stats]{Resolution: resolution, Items: items}, rows.Err()
}

func (s *Store) ContainerHistory(resolution string, from, to int64, limit int) (*HistoryResponse[[]container.Stats], error) {
	items := []HistoryItem[[]container.Stats]{}
	rows, err := s.db.Query(`
		SELECT captured_at, stats_json
		FROM container_stats_history
		WHERE resolution = ? AND captured_at >= ? AND captured_at <= ?
		ORDER BY captured_at ASC
		LIMIT ?
	`, resolution, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			capturedAt int64
			raw        string
		)
		if err := rows.Scan(&capturedAt, &raw); err != nil {
			return nil, err
		}
		var stats []container.Stats
		if err := json.Unmarshal([]byte(raw), &stats); err != nil {
			return nil, err
		}
		items = append(items, HistoryItem[[]container.Stats]{
			CapturedAt: capturedAt,
			Stats:      stats,
		})
	}
	return &HistoryResponse[[]container.Stats]{Resolution: resolution, Items: items}, rows.Err()
}

func (s *Store) currentCapturedAt() (int64, error) {
	var capturedAt int64
	err := s.db.QueryRow("SELECT captured_at FROM system_current WHERE singleton = 1").Scan(&capturedAt)
	return capturedAt, err
}

func currentSingleton[T any](s *Store, table string) (*CurrentDataResponse[T], error) {
	capturedAt, err := s.currentCapturedAt()
	if err != nil {
		return nil, err
	}

	var data T
	var raw string
	query := fmt.Sprintf("SELECT data_json FROM %s WHERE singleton = 1", table)
	err = s.db.QueryRow(query).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return &CurrentDataResponse[T]{CapturedAt: capturedAt, Data: data}, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return nil, err
	}
	return &CurrentDataResponse[T]{CapturedAt: capturedAt, Data: data}, nil
}

func marshalJSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func validResolution(resolution string) bool {
	_, ok := historyRetention[resolution]
	return ok
}

func retentionStrings() map[string]string {
	out := make(map[string]string, len(historyRetention))
	for resolution, duration := range historyRetention {
		out[resolution] = duration.String()
	}
	return out
}

func defaultMetaResponse(a *Agent, collectorInterval time.Duration) MetaResponse {
	smartRefreshInterval := ""
	if a.smartManager != nil {
		smartRefreshInterval = a.smartManager.refreshInterval.String()
	}
	return MetaResponse{
		Version:              version.Version,
		DataDir:              a.dataDir,
		DBPath:               a.store.Path(),
		ListenAddr:           a.ListenAddr(),
		CollectorInterval:    collectorInterval.String(),
		SmartRefreshInterval: smartRefreshInterval,
		Retention:            retentionStrings(),
	}
}
