package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mordilloSan/go-monitoring/internal/model/smart"
	"github.com/mordilloSan/go-monitoring/internal/model/systemd"
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

	summary, err := store.Summary()
	require.NoError(t, err)
	assert.Equal(t, capturedAt, summary.CapturedAt)
	assert.Equal(t, 42.0, summary.Stats.Cpu)
	require.Len(t, summary.Containers, 1)
	assert.Equal(t, "web", summary.Containers[0].Name)
	require.Len(t, summary.SystemdServices, 1)
	assert.Equal(t, "nginx.service", summary.SystemdServices[0].Name)

	containers, err := store.CurrentContainers()
	require.NoError(t, err)
	require.Len(t, containers.Items, 1)
	assert.Equal(t, "nginx", containers.Items[0].Image)

	systemdResp, err := store.CurrentSystemd()
	require.NoError(t, err)
	require.Len(t, systemdResp.Items, 1)
	assert.Equal(t, systemd.StatusActive, systemdResp.Items[0].State)

	processCount, err := store.CurrentProcessCount()
	require.NoError(t, err)
	assert.Equal(t, 2, processCount.Data.Total)

	processes, err := store.CurrentProcesses()
	require.NoError(t, err)
	require.Len(t, processes.Items, 1)
	assert.Equal(t, int32(10), processes.Items[0].PID)

	programs, err := store.CurrentPrograms()
	require.NoError(t, err)
	require.Len(t, programs.Items, 1)
	assert.Equal(t, "nginx", programs.Items[0].Name)

	connections, err := store.CurrentConnections()
	require.NoError(t, err)
	assert.Equal(t, 3, connections.Data.Total)

	irq, err := store.CurrentIRQ()
	require.NoError(t, err)
	require.Len(t, irq.Items, 1)
	assert.Equal(t, uint64(42), irq.Items[0].Total)

	smartResp, err := store.CurrentSmartDevices()
	require.NoError(t, err)
	require.Len(t, smartResp.Items, 1)
	assert.Equal(t, "/dev/sda", smartResp.Items[0].Data.DiskName)

	_, err = os.Stat(filepath.Join(tmpDir, "metrics.db"))
	require.NoError(t, err)
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
	require.Len(t, rolled.Items, 1)
	assert.InDelta(t, 50.0, rolled.Items[0].Stats.Cpu, 0.01)

	current, err := store.SystemHistory(resolution1m, 0, now.UnixMilli(), 20)
	require.NoError(t, err)
	require.NotEmpty(t, current.Items)
	for _, item := range current.Items {
		assert.GreaterOrEqual(t, item.CapturedAt, now.Add(-time.Hour).UnixMilli())
	}
}

func TestStoreMaintenanceAppliesRetentionToAllHistoryTables(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(tmpDir)
	require.NoError(t, err)
	defer store.Close()

	now := time.Now().UTC().Truncate(time.Minute)
	data := storetest.SampleCombinedData(42)
	systemStatsJSON, err := marshalJSON(data.Stats)
	require.NoError(t, err)
	containerStatsJSON, err := marshalJSON(data.Containers)
	require.NoError(t, err)

	for resolution, retention := range historyRetention {
		expiredAt := now.Add(-retention - time.Millisecond).UnixMilli()
		boundaryAt := now.Add(-retention).UnixMilli()

		_, err = store.db.Exec(`
			INSERT INTO system_stats_history (resolution, captured_at, stats_json)
			VALUES (?, ?, ?), (?, ?, ?)
		`, resolution, expiredAt, systemStatsJSON, resolution, boundaryAt, systemStatsJSON)
		require.NoError(t, err)

		_, err = store.db.Exec(`
			INSERT INTO container_stats_history (resolution, captured_at, stats_json)
			VALUES (?, ?, ?), (?, ?, ?)
		`, resolution, expiredAt, containerStatsJSON, resolution, boundaryAt, containerStatsJSON)
		require.NoError(t, err)
	}

	require.NoError(t, store.RunMaintenance(now))

	for resolution, retention := range historyRetention {
		cutoff := now.Add(-retention).UnixMilli()

		var expiredSystemRows int
		err = store.db.QueryRow(`
			SELECT COUNT(1)
			FROM system_stats_history
			WHERE resolution = ? AND captured_at < ?
		`, resolution, cutoff).Scan(&expiredSystemRows)
		require.NoError(t, err)
		assert.Zero(t, expiredSystemRows, "expired system rows should be deleted for %s", resolution)

		var expiredContainerRows int
		err = store.db.QueryRow(`
			SELECT COUNT(1)
			FROM container_stats_history
			WHERE resolution = ? AND captured_at < ?
		`, resolution, cutoff).Scan(&expiredContainerRows)
		require.NoError(t, err)
		assert.Zero(t, expiredContainerRows, "expired container rows should be deleted for %s", resolution)

		var keptSystemRows int
		err = store.db.QueryRow(`
			SELECT COUNT(1)
			FROM system_stats_history
			WHERE resolution = ? AND captured_at = ?
		`, resolution, cutoff).Scan(&keptSystemRows)
		require.NoError(t, err)
		assert.Equal(t, 1, keptSystemRows, "boundary system row should be kept for %s", resolution)

		var keptContainerRows int
		err = store.db.QueryRow(`
			SELECT COUNT(1)
			FROM container_stats_history
			WHERE resolution = ? AND captured_at = ?
		`, resolution, cutoff).Scan(&keptContainerRows)
		require.NoError(t, err)
		assert.Equal(t, 1, keptContainerRows, "boundary container row should be kept for %s", resolution)
	}
}
