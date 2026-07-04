package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mordilloSan/go-monitoring/internal/domain/smart"
	"github.com/mordilloSan/go-monitoring/internal/domain/system"
	"github.com/mordilloSan/go-monitoring/internal/domain/systemd"
	"github.com/mordilloSan/go-monitoring/internal/store/storetest"
)

func createStoreDBWithVersion(t *testing.T, dataDir string, version int) {
	t.Helper()

	db, err := sql.Open("sqlite", filepath.Join(dataDir, "metrics.db"))
	require.NoError(t, err)
	_, err = db.Exec(fmt.Sprintf("PRAGMA user_version = %d", version))
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

func TestStoreSnapshotAndCurrentQueries(t *testing.T) {
	ctx := context.Background()
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

	summaryCapturedAt, summary, err := store.Summary(ctx)
	require.NoError(t, err)
	assert.Equal(t, capturedAt, summaryCapturedAt)
	assert.InDelta(t, 42.0, summary.Stats.Cpu, 0.0001)
	require.Len(t, summary.Containers, 1)
	assert.Equal(t, "web", summary.Containers[0].Name)
	require.Len(t, summary.SystemdServices, 1)
	assert.Equal(t, "nginx.service", summary.SystemdServices[0].Name)

	systemSummaryCapturedAt, systemSummary, err := store.SystemSummary(ctx)
	require.NoError(t, err)
	assert.Equal(t, capturedAt, systemSummaryCapturedAt)
	assert.Equal(t, "host-a", systemSummary.Hostname)
	assert.Equal(t, "Test OS", systemSummary.OSName)
	assert.Equal(t, "cpu", systemSummary.CPUModel)
	assert.InDelta(t, 42.0, systemSummary.CPUPercent, 0.0001)
	assert.InDelta(t, 16.0, systemSummary.MemoryTotalGB, 0.0001)
	assert.InDelta(t, 8.0, systemSummary.MemoryUsedGB, 0.0001)
	assert.Equal(t, uint64(16*1024*1024*1024), systemSummary.MemoryBytes)

	containersCapturedAt, containers, err := store.CurrentContainers(ctx)
	require.NoError(t, err)
	assert.Equal(t, capturedAt, containersCapturedAt)
	require.Len(t, containers, 1)
	assert.Equal(t, "nginx", containers[0].Image)

	systemdCapturedAt, systemdItems, err := store.CurrentSystemd(ctx)
	require.NoError(t, err)
	assert.Equal(t, capturedAt, systemdCapturedAt)
	require.Len(t, systemdItems, 1)
	assert.Equal(t, systemd.StatusActive, systemdItems[0].State)

	processCapturedAt, processCount, err := store.CurrentProcessCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, capturedAt, processCapturedAt)
	assert.Equal(t, 2, processCount.Total)

	processesCapturedAt, processes, err := store.CurrentProcesses(ctx)
	require.NoError(t, err)
	assert.Equal(t, capturedAt, processesCapturedAt)
	require.Len(t, processes, 1)
	assert.Equal(t, int32(10), processes[0].PID)

	programsCapturedAt, programs, err := store.CurrentPrograms(ctx)
	require.NoError(t, err)
	assert.Equal(t, capturedAt, programsCapturedAt)
	require.Len(t, programs, 1)
	assert.Equal(t, "nginx", programs[0].Name)

	connectionsCapturedAt, connections, err := store.CurrentConnections(ctx)
	require.NoError(t, err)
	assert.Equal(t, capturedAt, connectionsCapturedAt)
	assert.Equal(t, 3, connections.Total)

	irqCapturedAt, irq, err := store.CurrentIRQ(ctx)
	require.NoError(t, err)
	assert.Equal(t, capturedAt, irqCapturedAt)
	require.Len(t, irq, 1)
	assert.Equal(t, uint64(42), irq[0].Total)

	smartCapturedAt, smartItems, err := store.CurrentSmartDevices(ctx)
	require.NoError(t, err)
	assert.Equal(t, capturedAt, smartCapturedAt)
	require.Len(t, smartItems, 1)
	assert.Equal(t, "/dev/sda", smartItems[0].Data.DiskName)

	cpuCapturedAt, cpuRaw, err := store.CurrentPlugin(ctx, PluginCPU)
	require.NoError(t, err)
	assert.Equal(t, capturedAt, cpuCapturedAt)
	assert.Contains(t, string(cpuRaw), `"cpu_percent":42`)

	allPlugins := PluginNames()
	require.Contains(t, allPlugins, PluginCPU)
	require.Contains(t, allPlugins, PluginSmart)

	_, err = os.Stat(filepath.Join(tmpDir, "metrics.db"))
	require.NoError(t, err)
}

func TestStoreRejectsNilSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(tmpDir)
	require.NoError(t, err)
	defer store.Close()

	err = store.WriteSnapshot(time.Now().UTC().UnixMilli(), nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "snapshot data is nil")
}

func TestStoreRejectsUnsupportedSchemaVersion(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "metrics.db")
	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	_, err = db.Exec(`
		PRAGMA user_version = 3;
	`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := OpenStore(tmpDir)
	require.Error(t, err)
	assert.Nil(t, store)
	assert.Contains(t, err.Error(), "unsupported store schema version 3")
}

func TestStoreMovesAsideCorruptDatabaseAndRecreatesSchema(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "metrics.db")
	require.NoError(t, os.WriteFile(dbPath, []byte("this is not sqlite"), 0600))
	require.NoError(t, os.WriteFile(dbPath+"-wal", []byte("stale wal"), 0600))
	require.NoError(t, os.WriteFile(dbPath+"-shm", []byte("stale shm"), 0600))

	store, err := OpenStore(tmpDir)
	require.NoError(t, err)
	defer store.Close()

	movedDBs, err := filepath.Glob(dbPath + ".corrupt-*")
	require.NoError(t, err)
	require.Len(t, movedDBs, 1)
	raw, err := os.ReadFile(movedDBs[0])
	require.NoError(t, err)
	assert.Equal(t, "this is not sqlite", string(raw))

	var version int
	require.NoError(t, store.db.QueryRow("PRAGMA user_version").Scan(&version))
	assert.Equal(t, storeSchemaVersion, version)
	require.NoError(t, store.WriteSnapshot(time.Now().UTC().UnixMilli(), storetest.SampleCombinedData(42)))
}

func TestStoreDatabaseIntegrityMaintenanceAndReset(t *testing.T) {
	tmpDir := t.TempDir()
	s, err := OpenStore(tmpDir)
	require.NoError(t, err)

	require.NoError(t, s.WriteSnapshot(time.Now().UTC().UnixMilli(), storetest.SampleCombinedData(42)))
	require.NoError(t, s.IntegrityCheck())
	require.NoError(t, s.Vacuum())
	require.NoError(t, CheckDatabase(tmpDir))
	require.NoError(t, s.Close())

	dbPath := filepath.Join(tmpDir, "metrics.db")
	resetStore, moved, err := ResetDatabase(tmpDir)
	require.NoError(t, err)
	defer resetStore.Close()
	require.NotEmpty(t, moved)
	assert.FileExists(t, dbPath)

	movedDBs, err := filepath.Glob(dbPath + ".reset-*")
	require.NoError(t, err)
	require.Len(t, movedDBs, 1)
	require.NoError(t, resetStore.IntegrityCheck())
	require.NoError(t, CheckDatabase(tmpDir))
}

func TestCheckDatabaseErrors(t *testing.T) {
	t.Run("missing database", func(t *testing.T) {
		err := CheckDatabase(t.TempDir())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "stat metrics.db")
	})

	t.Run("obsolete schema", func(t *testing.T) {
		tmpDir := t.TempDir()
		createStoreDBWithVersion(t, tmpDir, 3)

		err := CheckDatabase(tmpDir)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "obsolete metrics.db schema version 3")
	})

	t.Run("unsupported schema", func(t *testing.T) {
		tmpDir := t.TempDir()
		createStoreDBWithVersion(t, tmpDir, 99)

		err := CheckDatabase(tmpDir)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported metrics.db schema version 99")
	})
}

func TestStoreRepairValidDatabaseDoesNotMoveFiles(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(tmpDir)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	repaired, moved, err := RepairDatabase(tmpDir)

	require.NoError(t, err)
	defer repaired.Close()
	assert.Empty(t, moved)
	require.NoError(t, repaired.IntegrityCheck())
}

func TestStoreResetDatabaseWithoutExistingDB(t *testing.T) {
	tmpDir := t.TempDir()

	resetStore, moved, err := ResetDatabase(tmpDir)

	require.NoError(t, err)
	defer resetStore.Close()
	assert.Empty(t, moved)
	assert.FileExists(t, filepath.Join(tmpDir, "metrics.db"))
	require.NoError(t, resetStore.IntegrityCheck())
}

func TestStoreRepairMovesAsideCorruptDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "metrics.db")
	require.NoError(t, os.WriteFile(dbPath, []byte("this is not sqlite"), 0600))

	repaired, moved, err := RepairDatabase(tmpDir)
	require.NoError(t, err)
	defer repaired.Close()
	require.NotEmpty(t, moved)

	movedDBs, err := filepath.Glob(dbPath + ".repair-*")
	require.NoError(t, err)
	require.Len(t, movedDBs, 1)
	require.NoError(t, repaired.IntegrityCheck())
	require.NoError(t, CheckDatabase(tmpDir))
}

func TestStoreUnknownPluginsAndEmptySmartFallback(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(tmpDir)
	require.NoError(t, err)
	defer store.Close()

	capturedAt, smartRaw, err := store.CurrentPlugin(context.Background(), PluginSmart)
	require.NoError(t, err)
	assert.Zero(t, capturedAt)
	assert.JSONEq(t, `[]`, string(smartRaw))

	_, _, err = store.CurrentPlugin(context.Background(), "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown plugin "nope"`)

	_, err = store.PluginHistory(context.Background(), "nope", resolution1m, 0, 1, 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown plugin "nope"`)
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

	_, err = store.PluginHistory(context.Background(), PluginMem, resolution1m, 0, capturedAt, 10)
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

	rolled, err := store.SystemHistory(context.Background(), resolution10m, 0, now.UnixMilli(), 10)
	require.NoError(t, err)
	require.Len(t, rolled, 1)
	assert.InDelta(t, 50.0, rolled[0].Stats.Cpu, 0.01)

	current, err := store.SystemHistory(context.Background(), resolution1m, 0, now.UnixMilli(), 20)
	require.NoError(t, err)
	require.NotEmpty(t, current)
	for _, item := range current {
		assert.GreaterOrEqual(t, item.CapturedAt, now.Add(-time.Hour).UnixMilli())
	}
}

//nolint:gocognit // This test intentionally exercises several read APIs concurrently with writes.
func TestStoreConcurrentReadsDuringWriteSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(tmpDir, Options{HistoryPlugins: []string{PluginCPU}})
	require.NoError(t, err)
	defer store.Close()

	start := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, store.WriteSnapshot(start.UnixMilli(), storetest.SampleCombinedData(1)))

	errs := make(chan error, 128)
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 50 {
			capturedAt := start.Add(time.Duration(i+1) * time.Millisecond).UnixMilli()
			if writeErr := store.WriteSnapshot(capturedAt, storetest.SampleCombinedData(float64(i))); writeErr != nil {
				errs <- writeErr
			}
		}
	})

	for range 4 {
		wg.Go(func() {
			for range 50 {
				if _, _, readErr := store.CurrentPlugin(context.Background(), PluginCPU); readErr != nil {
					errs <- readErr
				}
				if _, _, readErr := store.SystemSummary(context.Background()); readErr != nil {
					errs <- readErr
				}
				if _, readErr := store.PluginHistory(context.Background(), PluginCPU, resolution1m, 0, time.Now().Add(time.Hour).UnixMilli(), 10); readErr != nil {
					errs <- readErr
				}
			}
		})
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

func TestStoreMaintenanceAtCollectorCadenceDoesNotDuplicateTenMinuteRollups(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(tmpDir, Options{HistoryPlugins: []string{PluginCPU}})
	require.NoError(t, err)
	defer store.Close()

	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	runFor := 12 * time.Hour
	tick := 15 * time.Second
	ticks := int(runFor / tick)

	for i := range ticks + 1 {
		now := start.Add(time.Duration(i) * tick)
		require.NoError(t, store.WriteSnapshot(now.UnixMilli(), storetest.SampleCombinedData(float64(i%100))))
		require.NoError(t, store.RunMaintenance(now))
	}

	var tenMinuteRows int
	err = store.db.QueryRow(`
		SELECT COUNT(1)
		FROM cpu_history
		WHERE resolution = ?
	`, resolution10m).Scan(&tenMinuteRows)
	require.NoError(t, err)

	assert.GreaterOrEqual(t, tenMinuteRows, 70)
	assert.LessOrEqual(t, tenMinuteRows, 73)
}

func TestStoreMaintenanceDoesNotRollFutureRowsIntoCurrentWindow(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(tmpDir, Options{HistoryPlugins: []string{PluginCPU}})
	require.NoError(t, err)
	defer store.Close()

	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	for i := range 8 {
		capturedAt := now.Add(-time.Duration(8-i) * time.Minute).UnixMilli()
		require.NoError(t, store.WriteSnapshot(capturedAt, storetest.SampleCombinedData(float64(i+1))))
	}
	require.NoError(t, store.WriteSnapshot(now.Add(time.Minute).UnixMilli(), storetest.SampleCombinedData(99)))

	require.NoError(t, store.RunMaintenance(now))

	var tenMinuteRows int
	err = store.db.QueryRow(`
		SELECT COUNT(1)
		FROM cpu_history
		WHERE resolution = ?
	`, resolution10m).Scan(&tenMinuteRows)
	require.NoError(t, err)
	assert.Zero(t, tenMinuteRows)
}

func TestStoreMaintenanceDelayedTickDoesNotSkipRollupWindows(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(tmpDir, Options{HistoryPlugins: []string{PluginCPU}})
	require.NoError(t, err)
	defer store.Close()

	start := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	for i := 1; i <= 10; i++ {
		require.NoError(t, store.WriteSnapshot(start.Add(time.Duration(i)*time.Minute).UnixMilli(), storetest.SampleCombinedData(float64(i))))
	}
	require.NoError(t, store.RunMaintenance(start.Add(10*time.Minute)))

	for i := 11; i <= 23; i++ {
		require.NoError(t, store.WriteSnapshot(start.Add(time.Duration(i)*time.Minute).UnixMilli(), storetest.SampleCombinedData(float64(i))))
	}
	// Maintenance is late: the next tick lands 3 minutes past the window edge.
	require.NoError(t, store.RunMaintenance(start.Add(23*time.Minute)))

	rows, err := store.db.Query(`
		SELECT captured_at
		FROM cpu_history
		WHERE resolution = ?
		ORDER BY captured_at ASC
	`, resolution10m)
	require.NoError(t, err)
	defer rows.Close()

	var capturedAts []int64
	for rows.Next() {
		var capturedAt int64
		require.NoError(t, rows.Scan(&capturedAt))
		capturedAts = append(capturedAts, capturedAt)
	}
	require.NoError(t, rows.Err())

	// Windows must chain off the previous rollup, not the delayed tick time,
	// so no 1m row falls between two aggregated windows.
	assert.Equal(t, []int64{
		start.Add(10 * time.Minute).UnixMilli(),
		start.Add(20 * time.Minute).UnixMilli(),
	}, capturedAts)
}

func TestStoreMaintenanceAppliesRetentionToAllHistoryTables(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(tmpDir, Options{HistoryPlugins: PluginNames()})
	require.NoError(t, err)
	defer store.Close()

	now := time.Now().UTC().Truncate(time.Minute)
	historyJSON := `{"retention":"test"}`

	for resolution, retention := range historyRetention {
		expiredAt := now.Add(-retention - time.Millisecond).UnixMilli()
		boundaryAt := now.Add(-retention).UnixMilli()

		for _, plugin := range PluginNames() {
			_, err = store.db.Exec(fmt.Sprintf(`
				INSERT INTO %s (resolution, captured_at, stats_json)
				VALUES (?, ?, ?), (?, ?, ?)
			`, pluginHistoryTable(plugin)), resolution, expiredAt, historyJSON, resolution, boundaryAt, historyJSON)
			require.NoError(t, err)
		}
	}

	require.NoError(t, store.RunMaintenance(now))

	for resolution, retention := range historyRetention {
		cutoff := now.Add(-retention).UnixMilli()

		for _, plugin := range PluginNames() {
			var expiredRows int
			err = store.db.QueryRow(fmt.Sprintf(`
				SELECT COUNT(1)
				FROM %s
				WHERE resolution = ? AND captured_at < ?
			`, pluginHistoryTable(plugin)), resolution, cutoff).Scan(&expiredRows)
			require.NoError(t, err)
			assert.Zero(t, expiredRows, "expired %s rows should be deleted for %s", plugin, resolution)

			var keptRows int
			err = store.db.QueryRow(fmt.Sprintf(`
				SELECT COUNT(1)
				FROM %s
				WHERE resolution = ? AND captured_at = ?
			`, pluginHistoryTable(plugin)), resolution, cutoff).Scan(&keptRows)
			require.NoError(t, err)
			assert.Equal(t, 1, keptRows, "boundary %s row should be kept for %s", plugin, resolution)
		}
	}
}

func TestAggregatePluginHistoryJSONCoversAllPlugins(t *testing.T) {
	first := storetest.SampleCombinedData(10)
	first.Stats.Swap = 2
	first.Stats.SwapUsed = 1
	first.Stats.NetworkSent = 3
	first.Stats.NetworkRecv = 4
	first.Stats.Temperatures = map[string]float64{"cpu": 50}
	first.Stats.GPUData = map[string]system.GPUData{"0": {Name: "gpu0", Usage: 20, MemoryUsed: 100}}
	second := storetest.SampleCombinedData(30)
	second.Stats.Swap = 4
	second.Stats.SwapUsed = 2
	second.Stats.NetworkSent = 9
	second.Stats.NetworkRecv = 10
	second.Stats.Temperatures = map[string]float64{"cpu": 70}
	second.Stats.GPUData = map[string]system.GPUData{"0": {Name: "gpu0", Usage: 60, MemoryUsed: 300}}

	firstPayloads := snapshotPluginPayloads(first)
	secondPayloads := snapshotPluginPayloads(second)
	passThroughPlugins := map[string]struct{}{
		PluginSystemd:     {},
		PluginProcesses:   {},
		PluginPrograms:    {},
		PluginConnections: {},
		PluginIRQ:         {},
	}

	for _, plugin := range PluginNames() {
		t.Run(plugin, func(t *testing.T) {
			firstRaw := `{"first":true}`
			secondRaw := `{"second":true}`
			if plugin != PluginSmart {
				var err error
				firstRaw, err = marshalJSON(firstPayloads[plugin])
				require.NoError(t, err)
				secondRaw, err = marshalJSON(secondPayloads[plugin])
				require.NoError(t, err)
			}

			aggregated, err := aggregatePluginHistoryJSON(plugin, []string{firstRaw, secondRaw})

			require.NoError(t, err)
			assert.True(t, json.Valid([]byte(aggregated)), "aggregate should be valid JSON: %s", aggregated)
			if _, passThrough := passThroughPlugins[plugin]; passThrough || plugin == PluginSmart {
				assert.JSONEq(t, secondRaw, aggregated)
			}
		})
	}
}
