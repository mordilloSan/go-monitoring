// Package store persists collected metrics to a local SQLite database and
// serves history/current snapshot reads back to API consumers.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mordilloSan/go-monitoring/internal/domain/container"
	modelnet "github.com/mordilloSan/go-monitoring/internal/domain/network"
	procmodel "github.com/mordilloSan/go-monitoring/internal/domain/process"
	"github.com/mordilloSan/go-monitoring/internal/domain/smart"
	"github.com/mordilloSan/go-monitoring/internal/domain/system"
	"github.com/mordilloSan/go-monitoring/internal/domain/systemd"
	"github.com/mordilloSan/go-monitoring/internal/utils"
	_ "modernc.org/sqlite"
)

const storeSchemaVersion = 4

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
	db             *sql.DB
	path           string
	historyPlugins map[string]struct{}
}

type HistoryRecord[T any] struct {
	CapturedAt int64
	Stats      T
}

type SmartDeviceRecord struct {
	ID   string
	Key  string
	Data smart.SmartData
}

type containerCurrentRecord struct {
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

type Options struct {
	HistoryPlugins []string
}

func OpenStore(dataDir string, options ...Options) (*Store, error) {
	dbPath := filepath.Join(dataDir, "metrics.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	historyPlugins := DefaultHistoryPluginNames()
	if len(options) > 0 {
		historyPlugins = options[0].HistoryPlugins
	}
	store := &Store{
		db:             db,
		path:           dbPath,
		historyPlugins: historyPluginSet(historyPlugins),
	}
	if err := store.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func ParseHistoryPlugins(raw string, explicit bool) ([]string, error) {
	return parseHistoryPlugins(raw, explicit, utils.GetEnv)
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
		return s.createSchema()
	case storeSchemaVersion:
		return s.createSchema()
	case 1, 2, 3:
		slog.Warn("Resetting metrics.db for plugin storage schema", "from_version", version, "to_version", storeSchemaVersion)
		return s.resetSchema()
	default:
		return fmt.Errorf("unsupported store schema version %d", version)
	}
}

func (s *Store) resetSchema() error {
	for _, table := range knownStoreTables() {
		if _, err := s.db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", table)); err != nil {
			return err
		}
	}
	return s.createSchema()
}

func (s *Store) createSchema() error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
	}
	for _, plugin := range pluginNames {
		statements = append(statements,
			fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
				singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
				captured_at INTEGER NOT NULL,
				data_json TEXT NOT NULL
			)`, pluginCurrentTable(plugin)),
			fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
				resolution TEXT NOT NULL,
				captured_at INTEGER NOT NULL,
				stats_json TEXT NOT NULL,
				PRIMARY KEY (resolution, captured_at)
			)`, pluginHistoryTable(plugin)),
			fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_%s_captured_at
				ON %s (captured_at)`, pluginHistoryTable(plugin), pluginHistoryTable(plugin)),
		)
	}
	statements = append(statements, fmt.Sprintf("PRAGMA user_version = %d", storeSchemaVersion))

	for _, stmt := range statements {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func knownStoreTables() []string {
	tables := []string{
		"meta",
		"system_current",
		"system_stats_history",
		"container_stats_history",
		"containers_current",
		"systemd_services_current",
		"smart_devices_current",
		"processes_current",
		"process_count_current",
		"programs_current",
		"connections_current",
		"irq_current",
	}
	for _, plugin := range pluginNames {
		tables = append(tables, pluginCurrentTable(plugin), pluginHistoryTable(plugin))
	}
	return tables
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

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if err = s.writeSnapshotPluginRows(tx, capturedAt, data); err != nil {
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

func (s *Store) writeSnapshotPluginRows(tx *sql.Tx, capturedAt int64, data *system.CombinedData) error {
	payloads := snapshotPluginPayloads(data)
	for _, plugin := range pluginNames {
		if plugin == PluginSmart {
			continue
		}
		payload, ok := payloads[plugin]
		if !ok {
			continue
		}
		if err := writePluginSnapshot(tx, plugin, capturedAt, payload, s.HistoryEnabled(plugin)); err != nil {
			return err
		}
	}

	infoJSON, err := marshalJSON(data.Info)
	if err != nil {
		return err
	}
	if err := upsertMeta(tx, "last_info_json", infoJSON); err != nil {
		return err
	}
	if data.Details != nil {
		detailsJSON, err := marshalJSON(data.Details)
		if err != nil {
			return err
		}
		if err := upsertMeta(tx, "last_details_json", detailsJSON); err != nil {
			return err
		}
	}
	return nil
}

func writePluginSnapshot(tx *sql.Tx, plugin string, capturedAt int64, payload any, historyEnabled bool) error {
	raw, err := marshalJSON(payload)
	if err != nil {
		return err
	}
	if err := replacePluginCurrent(tx, plugin, capturedAt, raw); err != nil {
		return err
	}
	if historyEnabled {
		return insertPluginHistory(tx, plugin, resolution1m, capturedAt, raw)
	}
	return nil
}

func replacePluginCurrent(tx *sql.Tx, plugin string, capturedAt int64, raw string) error {
	_, err := tx.Exec(fmt.Sprintf(`
		INSERT INTO %s (singleton, captured_at, data_json)
		VALUES (1, ?, ?)
		ON CONFLICT(singleton) DO UPDATE SET
			captured_at = excluded.captured_at,
			data_json = excluded.data_json
	`, pluginCurrentTable(plugin)), capturedAt, raw)
	return err
}

func insertPluginHistory(tx *sql.Tx, plugin, resolution string, capturedAt int64, raw string) error {
	_, err := tx.Exec(fmt.Sprintf(`
		INSERT OR REPLACE INTO %s (resolution, captured_at, stats_json)
		VALUES (?, ?, ?)
	`, pluginHistoryTable(plugin)), resolution, capturedAt, raw)
	return err
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

	currentItems := make([]SmartDeviceRecord, 0, len(items))
	for key, item := range items {
		id := key
		if item.DiskName != "" {
			id = item.DiskName
		}
		currentItems = append(currentItems, SmartDeviceRecord{
			ID:   id,
			Key:  key,
			Data: item,
		})
	}

	raw, err := marshalJSON(currentItems)
	if err != nil {
		return err
	}
	if err = replacePluginCurrent(tx, PluginSmart, capturedAt, raw); err != nil {
		return err
	}
	if s.HistoryEnabled(PluginSmart) {
		if err = insertPluginHistory(tx, PluginSmart, resolution1m, capturedAt, raw); err != nil {
			return err
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

func (s *Store) Summary() (int64, *system.CombinedData, error) {
	capturedAt, err := s.currentCapturedAt()
	if err != nil {
		return 0, nil, err
	}

	var summary system.CombinedData
	if infoRaw, ok, err := s.metaValue("last_info_json"); err != nil {
		return 0, nil, err
	} else if ok {
		if err := json.Unmarshal([]byte(infoRaw), &summary.Info); err != nil {
			return 0, nil, err
		}
	}
	if detailsRaw, ok, err := s.metaValue("last_details_json"); err != nil {
		return 0, nil, err
	} else if ok && detailsRaw != "" {
		summary.Details = &system.Details{}
		if err := json.Unmarshal([]byte(detailsRaw), summary.Details); err != nil {
			return 0, nil, err
		}
	}

	for _, plugin := range pluginNames {
		if plugin == PluginSmart {
			continue
		}
		_, raw, err := s.CurrentPlugin(plugin)
		if err != nil {
			return 0, nil, err
		}
		if err := applyPluginPayload(plugin, raw, &summary); err != nil {
			return 0, nil, err
		}
	}
	return capturedAt, &summary, nil
}

func (s *Store) currentContainerStats() ([]*container.Stats, error) {
	currentItems := []containerCurrentRecord{}
	_, raw, err := s.CurrentPlugin(PluginContainers)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &currentItems); err != nil {
		return nil, err
	}
	items := make([]*container.Stats, 0, len(currentItems))
	for _, current := range currentItems {
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
	return items, nil
}

func (s *Store) CurrentContainers() (int64, []*container.Stats, error) {
	capturedAt, err := s.currentCapturedAt()
	if err != nil {
		return 0, nil, err
	}
	items, err := s.currentContainerStats()
	return capturedAt, items, err
}

func (s *Store) CurrentSystemd() (int64, []*systemd.Service, error) {
	capturedAt, raw, err := s.CurrentPlugin(PluginSystemd)
	if err != nil {
		return 0, nil, err
	}
	items := []*systemd.Service{}
	if err := json.Unmarshal(raw, &items); err != nil {
		return 0, nil, err
	}
	return capturedAt, items, nil
}

func (s *Store) CurrentSystemdItems() ([]*systemd.Service, error) {
	_, items, err := s.CurrentSystemd()
	return items, err
}

func (s *Store) CurrentProcessCount() (int64, procmodel.Count, error) {
	capturedAt, raw, err := s.CurrentPlugin(PluginProcesses)
	if err != nil {
		return 0, procmodel.Count{}, err
	}
	var data ProcessesData
	if err := json.Unmarshal(raw, &data); err != nil {
		return 0, procmodel.Count{}, err
	}
	if data.Count == nil {
		return capturedAt, procmodel.Count{}, nil
	}
	return capturedAt, *data.Count, nil
}

func (s *Store) CurrentProcesses() (int64, []procmodel.Process, error) {
	capturedAt, raw, err := s.CurrentPlugin(PluginProcesses)
	if err != nil {
		return 0, nil, err
	}
	var data ProcessesData
	if err := json.Unmarshal(raw, &data); err != nil {
		return 0, nil, err
	}
	return capturedAt, data.Items, nil
}

func (s *Store) CurrentPrograms() (int64, []procmodel.Program, error) {
	capturedAt, raw, err := s.CurrentPlugin(PluginPrograms)
	if err != nil {
		return 0, nil, err
	}
	items := []procmodel.Program{}
	if err := json.Unmarshal(raw, &items); err != nil {
		return 0, nil, err
	}
	return capturedAt, items, nil
}

func (s *Store) CurrentConnections() (int64, modelnet.ConnectionStats, error) {
	capturedAt, raw, err := s.CurrentPlugin(PluginConnections)
	if err != nil {
		return 0, modelnet.ConnectionStats{}, err
	}
	var data modelnet.ConnectionStats
	if string(raw) == "null" {
		return capturedAt, data, nil
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return 0, data, err
	}
	return capturedAt, data, nil
}

func (s *Store) CurrentIRQ() (int64, []modelnet.IRQStat, error) {
	capturedAt, raw, err := s.CurrentPlugin(PluginIRQ)
	if err != nil {
		return 0, nil, err
	}
	items := []modelnet.IRQStat{}
	if err := json.Unmarshal(raw, &items); err != nil {
		return 0, nil, err
	}
	return capturedAt, items, nil
}

func (s *Store) CurrentSmartDevices() (int64, []SmartDeviceRecord, error) {
	capturedAt, raw, err := s.CurrentPlugin(PluginSmart)
	if err != nil {
		return 0, nil, err
	}
	items := []SmartDeviceRecord{}
	if err := json.Unmarshal(raw, &items); err != nil {
		return 0, nil, err
	}
	return capturedAt, items, nil
}

func (s *Store) SystemHistory(resolution string, from, to int64, limit int) ([]HistoryRecord[system.Stats], error) {
	rawItems, err := s.PluginHistory(PluginCPU, resolution, from, to, limit)
	if err != nil {
		return nil, err
	}
	items := make([]HistoryRecord[system.Stats], 0, len(rawItems))
	for _, rawItem := range rawItems {
		data := system.CombinedData{}
		if err := applyPluginPayload(PluginCPU, rawItem.Stats, &data); err != nil {
			return nil, err
		}
		items = append(items, HistoryRecord[system.Stats]{
			CapturedAt: rawItem.CapturedAt,
			Stats:      data.Stats,
		})
	}
	return items, nil
}

func (s *Store) ContainerHistory(resolution string, from, to int64, limit int) ([]HistoryRecord[[]container.Stats], error) {
	rawItems, err := s.PluginHistory(PluginContainers, resolution, from, to, limit)
	if err != nil {
		return nil, err
	}
	items := make([]HistoryRecord[[]container.Stats], 0, len(rawItems))
	for _, rawItem := range rawItems {
		var stats []container.Stats
		if err := json.Unmarshal(rawItem.Stats, &stats); err != nil {
			return nil, err
		}
		items = append(items, HistoryRecord[[]container.Stats]{
			CapturedAt: rawItem.CapturedAt,
			Stats:      stats,
		})
	}
	return items, nil
}

func (s *Store) currentCapturedAt() (int64, error) {
	raw, ok, err := s.metaValue("last_persisted_at")
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, sql.ErrNoRows
	}
	return strconv.ParseInt(raw, 10, 64)
}

func (s *Store) CurrentPlugin(plugin string) (int64, json.RawMessage, error) {
	if !IsPluginName(plugin) {
		return 0, nil, fmt.Errorf("unknown plugin %q", plugin)
	}
	var (
		capturedAt int64
		raw        string
	)
	err := s.db.QueryRow(fmt.Sprintf(`
		SELECT captured_at, data_json
		FROM %s
		WHERE singleton = 1
	`, pluginCurrentTable(plugin))).Scan(&capturedAt, &raw)
	if errors.Is(err, sql.ErrNoRows) && plugin == PluginSmart {
		return 0, json.RawMessage("[]"), nil
	}
	if err != nil {
		return 0, nil, err
	}
	return capturedAt, json.RawMessage(raw), nil
}

func (s *Store) PluginHistory(plugin, resolution string, from, to int64, limit int) ([]HistoryRecord[json.RawMessage], error) {
	if !IsPluginName(plugin) {
		return nil, fmt.Errorf("unknown plugin %q", plugin)
	}
	if !s.HistoryEnabled(plugin) {
		return nil, sql.ErrNoRows
	}
	items := []HistoryRecord[json.RawMessage]{}
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT captured_at, stats_json
		FROM %s
		WHERE resolution = ? AND captured_at >= ? AND captured_at <= ?
		ORDER BY captured_at ASC
		LIMIT ?
	`, pluginHistoryTable(plugin)), resolution, from, to, limit)
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
		items = append(items, HistoryRecord[json.RawMessage]{
			CapturedAt: capturedAt,
			Stats:      json.RawMessage(raw),
		})
	}
	return items, rows.Err()
}

func (s *Store) HistoryEnabled(plugin string) bool {
	_, ok := s.historyPlugins[plugin]
	return ok
}

func (s *Store) historyPluginNames() []string {
	out := make([]string, 0, len(s.historyPlugins))
	for _, plugin := range pluginNames {
		if s.HistoryEnabled(plugin) {
			out = append(out, plugin)
		}
	}
	return out
}

func (s *Store) metaValue(key string) (string, bool, error) {
	var value string
	err := s.db.QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

func marshalJSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func ValidResolution(resolution string) bool {
	_, ok := historyRetention[resolution]
	return ok
}

// RetentionStrings returns history retention windows keyed by resolution,
// with each duration formatted via time.Duration.String.
func RetentionStrings() map[string]string {
	out := make(map[string]string, len(historyRetention))
	for resolution, duration := range historyRetention {
		out[resolution] = duration.String()
	}
	return out
}
