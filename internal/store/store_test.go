package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mordilloSan/go-monitoring/internal/domain/smart"
	"github.com/mordilloSan/go-monitoring/internal/domain/systemd"
	"github.com/mordilloSan/go-monitoring/internal/store/storetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreSnapshotAndCurrentQueries(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(tmpDir)
	require.NoError(t, err)
	defer store.Close()

	capturedAt := time.Now().UTC().UnixMilli()
	require.NoError(t, store.WriteSnapshot(capturedAt, storetest.SampleCombinedData(42)))
	require.NoError(t, store.WriteSmartDevices(capturedAt, map[string]smart.SmartData{
		"/dev/sda": {
			ModelName:   "disk-a",
			DiskName:    "/dev/sda",
			SmartStatus: "passed",
		},
	}))

	summaryCapturedAt, summary, err := store.Summary()
	require.NoError(t, err)
	assert.Equal(t, capturedAt, summaryCapturedAt)
	assert.Equal(t, 42.0, summary.Stats.Cpu)
	require.Len(t, summary.Containers, 1)
	assert.Equal(t, "web", summary.Containers[0].Name)
	require.Len(t, summary.SystemdServices, 1)
	assert.Equal(t, "nginx.service", summary.SystemdServices[0].Name)

	systemSummaryCapturedAt, systemSummary, err := store.SystemSummary()
	require.NoError(t, err)
	assert.Equal(t, capturedAt, systemSummaryCapturedAt)
	assert.Equal(t, "host-a", systemSummary.Hostname)
	assert.Equal(t, "Test OS", systemSummary.OSName)
	assert.Equal(t, "cpu", systemSummary.CPUModel)
	assert.Equal(t, 42.0, systemSummary.CPUPercent)
	assert.Equal(t, 16.0, systemSummary.MemoryTotalGB)
	assert.Equal(t, 8.0, systemSummary.MemoryUsedGB)
	assert.Equal(t, uint64(16*1024*1024*1024), systemSummary.MemoryBytes)

	containersCapturedAt, containers, err := store.CurrentContainers()
	require.NoError(t, err)
	assert.Equal(t, capturedAt, containersCapturedAt)
	require.Len(t, containers, 1)
	assert.Equal(t, "nginx", containers[0].Image)

	systemdCapturedAt, systemdItems, err := store.CurrentSystemd()
	require.NoError(t, err)
	assert.Equal(t, capturedAt, systemdCapturedAt)
	require.Len(t, systemdItems, 1)
	assert.Equal(t, systemd.StatusActive, systemdItems[0].State)

	processCapturedAt, processCount, err := store.CurrentProcessCount()
	require.NoError(t, err)
	assert.Equal(t, capturedAt, processCapturedAt)
	assert.Equal(t, 2, processCount.Total)

	processesCapturedAt, processes, err := store.CurrentProcesses()
	require.NoError(t, err)
	assert.Equal(t, capturedAt, processesCapturedAt)
	require.Len(t, processes, 1)
	assert.Equal(t, int32(10), processes[0].PID)

	programsCapturedAt, programs, err := store.CurrentPrograms()
	require.NoError(t, err)
	assert.Equal(t, capturedAt, programsCapturedAt)
	require.Len(t, programs, 1)
	assert.Equal(t, "nginx", programs[0].Name)

	connectionsCapturedAt, connections, err := store.CurrentConnections()
	require.NoError(t, err)
	assert.Equal(t, capturedAt, connectionsCapturedAt)
	assert.Equal(t, 3, connections.Total)

	irqCapturedAt, irq, err := store.CurrentIRQ()
	require.NoError(t, err)
	assert.Equal(t, capturedAt, irqCapturedAt)
	require.Len(t, irq, 1)
	assert.Equal(t, uint64(42), irq[0].Total)

	smartCapturedAt, smartItems, err := store.CurrentSmartDevices()
	require.NoError(t, err)
	assert.Equal(t, capturedAt, smartCapturedAt)
	require.Len(t, smartItems, 1)
	assert.Equal(t, "/dev/sda", smartItems[0].Data.DiskName)

	cpuCapturedAt, cpuRaw, err := store.CurrentPlugin(PluginCPU)
	require.NoError(t, err)
	assert.Equal(t, capturedAt, cpuCapturedAt)
	assert.Contains(t, string(cpuRaw), `"cpu_percent":42`)

	allPlugins := PluginNames()
	require.Contains(t, allPlugins, PluginCPU)
	require.Contains(t, allPlugins, PluginSmart)

	_, err = os.Stat(filepath.Join(tmpDir, "metrics.db"))
	require.NoError(t, err)
}

func TestStoreResetsOldSchemaToPluginTables(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "metrics.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = db.Exec(`
		CREATE TABLE system_current (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			captured_at INTEGER NOT NULL,
			info_json TEXT NOT NULL,
			stats_json TEXT NOT NULL,
			details_json TEXT
		);
		PRAGMA user_version = 3;
	`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := OpenStore(tmpDir)
	require.NoError(t, err)
	defer store.Close()

	var oldTableCount int
	err = store.db.QueryRow("SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 'system_current'").Scan(&oldTableCount)
	require.NoError(t, err)
	assert.Zero(t, oldTableCount)

	var newTableCount int
	err = store.db.QueryRow("SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 'cpu_current'").Scan(&newTableCount)
	require.NoError(t, err)
	assert.Equal(t, 1, newTableCount)

	var version int
	require.NoError(t, store.db.QueryRow("PRAGMA user_version").Scan(&version))
	assert.Equal(t, storeSchemaVersion, version)
}

func TestStoreWritesHistoryOnlyForAllowlistedPlugins(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(tmpDir, Options{HistoryPlugins: []string{PluginCPU}})
	require.NoError(t, err)
	defer store.Close()

	capturedAt := time.Now().UTC().UnixMilli()
	require.NoError(t, store.WriteSnapshot(capturedAt, storetest.SampleCombinedData(42)))

	var cpuRows int
	require.NoError(t, store.db.QueryRow("SELECT COUNT(1) FROM cpu_history").Scan(&cpuRows))
	assert.Equal(t, 1, cpuRows)

	var memRows int
	require.NoError(t, store.db.QueryRow("SELECT COUNT(1) FROM mem_history").Scan(&memRows))
	assert.Zero(t, memRows)

	_, err = store.PluginHistory(PluginMem, resolution1m, 0, capturedAt, 10)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestParseHistoryPlugins(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		plugins, err := ParseHistoryPlugins("", false)
		require.NoError(t, err)
		assert.Equal(t, DefaultHistoryPluginNames(), plugins)
	})

	t.Run("env fallback", func(t *testing.T) {
		t.Setenv("HISTORY", "cpu,swap")
		plugins, err := ParseHistoryPlugins("", false)
		require.NoError(t, err)
		assert.Equal(t, []string{PluginCPU, PluginSwap}, plugins)
	})

	t.Run("explicit all", func(t *testing.T) {
		plugins, err := ParseHistoryPlugins("all", true)
		require.NoError(t, err)
		assert.Equal(t, PluginNames(), plugins)
	})

	t.Run("explicit none", func(t *testing.T) {
		plugins, err := ParseHistoryPlugins("none", true)
		require.NoError(t, err)
		assert.Empty(t, plugins)
	})

	t.Run("invalid plugin", func(t *testing.T) {
		_, err := ParseHistoryPlugins("cpu,nope", true)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown history plugin "nope"`)
	})
}

func TestStoreMaintenanceCreatesRollupsAndDeletesExpiredRows(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(tmpDir)
	require.NoError(t, err)
	defer store.Close()

	now := time.Now().UTC().Truncate(time.Minute)
	for i := range 9 {
		capturedAt := now.Add(-time.Duration(9-i) * time.Minute).UnixMilli()
		require.NoError(t, store.WriteSnapshot(capturedAt, storetest.SampleCombinedData(float64((i+1)*10))))
	}
	oldCapturedAt := now.Add(-2 * time.Hour).UnixMilli()
	require.NoError(t, store.WriteSnapshot(oldCapturedAt, storetest.SampleCombinedData(5)))

	require.NoError(t, store.RunMaintenance(now))

	rolled, err := store.SystemHistory(resolution10m, 0, now.UnixMilli(), 10)
	require.NoError(t, err)
	require.Len(t, rolled, 1)
	assert.InDelta(t, 50.0, rolled[0].Stats.Cpu, 0.01)

	current, err := store.SystemHistory(resolution1m, 0, now.UnixMilli(), 20)
	require.NoError(t, err)
	require.NotEmpty(t, current)
	for _, item := range current {
		assert.GreaterOrEqual(t, item.CapturedAt, now.Add(-time.Hour).UnixMilli())
	}
}

func TestStoreMaintenanceAppliesRetentionToAllHistoryTables(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(tmpDir, Options{HistoryPlugins: []string{PluginCPU, PluginContainers}})
	require.NoError(t, err)
	defer store.Close()

	now := time.Now().UTC().Truncate(time.Minute)
	data := storetest.SampleCombinedData(42)
	payloads := snapshotPluginPayloads(data)
	cpuJSON, err := marshalJSON(payloads[PluginCPU])
	require.NoError(t, err)
	containerJSON, err := marshalJSON(payloads[PluginContainers])
	require.NoError(t, err)

	for resolution, retention := range historyRetention {
		expiredAt := now.Add(-retention - time.Millisecond).UnixMilli()
		boundaryAt := now.Add(-retention).UnixMilli()

		_, err = store.db.Exec(`
			INSERT INTO cpu_history (resolution, captured_at, stats_json)
			VALUES (?, ?, ?), (?, ?, ?)
		`, resolution, expiredAt, cpuJSON, resolution, boundaryAt, cpuJSON)
		require.NoError(t, err)

		_, err = store.db.Exec(`
			INSERT INTO containers_history (resolution, captured_at, stats_json)
			VALUES (?, ?, ?), (?, ?, ?)
		`, resolution, expiredAt, containerJSON, resolution, boundaryAt, containerJSON)
		require.NoError(t, err)
	}

	require.NoError(t, store.RunMaintenance(now))

	for resolution, retention := range historyRetention {
		cutoff := now.Add(-retention).UnixMilli()

		var expiredSystemRows int
		err = store.db.QueryRow(`
			SELECT COUNT(1)
			FROM cpu_history
			WHERE resolution = ? AND captured_at < ?
		`, resolution, cutoff).Scan(&expiredSystemRows)
		require.NoError(t, err)
		assert.Zero(t, expiredSystemRows, "expired system rows should be deleted for %s", resolution)

		var expiredContainerRows int
		err = store.db.QueryRow(`
			SELECT COUNT(1)
			FROM containers_history
			WHERE resolution = ? AND captured_at < ?
		`, resolution, cutoff).Scan(&expiredContainerRows)
		require.NoError(t, err)
		assert.Zero(t, expiredContainerRows, "expired container rows should be deleted for %s", resolution)

		var keptSystemRows int
		err = store.db.QueryRow(`
			SELECT COUNT(1)
			FROM cpu_history
			WHERE resolution = ? AND captured_at = ?
		`, resolution, cutoff).Scan(&keptSystemRows)
		require.NoError(t, err)
		assert.Equal(t, 1, keptSystemRows, "boundary system row should be kept for %s", resolution)

		var keptContainerRows int
		err = store.db.QueryRow(`
			SELECT COUNT(1)
			FROM containers_history
			WHERE resolution = ? AND captured_at = ?
		`, resolution, cutoff).Scan(&keptContainerRows)
		require.NoError(t, err)
		assert.Equal(t, 1, keptContainerRows, "boundary container row should be kept for %s", resolution)
	}
}
