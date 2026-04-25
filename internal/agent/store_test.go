package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mordilloSan/go-monitoring/internal/model/container"
	modelnet "github.com/mordilloSan/go-monitoring/internal/model/network"
	procmodel "github.com/mordilloSan/go-monitoring/internal/model/process"
	"github.com/mordilloSan/go-monitoring/internal/model/smart"
	"github.com/mordilloSan/go-monitoring/internal/model/system"
	"github.com/mordilloSan/go-monitoring/internal/model/systemd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleCombinedData(cpu float64) *system.CombinedData {
	return &system.CombinedData{
		Stats: system.Stats{
			Cpu:       cpu,
			Mem:       16,
			MemUsed:   8,
			MemPct:    50,
			DiskTotal: 100,
			DiskUsed:  40,
			DiskPct:   40,
			LoadAvg:   [3]float64{1, 2, 3},
			Bandwidth: [2]uint64{1000, 2000},
		},
		Info: system.Info{
			Uptime:       100,
			Cpu:          cpu,
			MemPct:       50,
			DiskPct:      40,
			AgentVersion: "test",
		},
		Containers: []*container.Stats{
			{
				Id:        "c1",
				Name:      "web",
				Image:     "nginx",
				Status:    "running",
				Health:    container.DockerHealthHealthy,
				Cpu:       cpu / 2,
				Mem:       1,
				Bandwidth: [2]uint64{200, 100},
			},
		},
		SystemdServices: []*systemd.Service{
			{
				Name:  "nginx.service",
				State: systemd.StatusActive,
				Sub:   systemd.SubStateRunning,
				Cpu:   cpu / 4,
				Mem:   1024,
			},
		},
		ProcessCount: &procmodel.Count{Total: 2, Running: 1, Sleeping: 1, Thread: 4, PIDMax: 4194304},
		Processes: []procmodel.Process{
			{PID: 10, Name: "nginx", Status: "running", CPUPercent: cpu, MemoryPercent: 1.25, NumThreads: 2},
		},
		Programs: []procmodel.Program{
			{Name: "nginx", Count: 1, CPUPercent: cpu, MemoryPercent: 1.25, MemoryRSSBytes: 1024, PIDs: []int32{10}},
		},
		Connections: &modelnet.ConnectionStats{
			NetConnectionsEnabled: true,
			NFConntrackEnabled:    true,
			Total:                 3,
			TCP:                   2,
			UDP:                   1,
			Listen:                1,
			NFConntrackCount:      7,
			NFConntrackMax:        100,
			NFConntrackPercent:    7,
		},
		IRQs: []modelnet.IRQStat{
			{IRQ: "0", Total: 42, CPUCounts: []uint64{40, 2}, Description: "timer"},
		},
		Details: &system.Details{
			Hostname:    "host-a",
			CpuModel:    "cpu",
			MemoryTotal: 16 * 1024 * 1024 * 1024,
		},
	}
}

func TestStoreSnapshotAndCurrentQueries(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := OpenStore(tmpDir)
	require.NoError(t, err)
	defer store.Close()

	capturedAt := time.Now().UTC().UnixMilli()
	require.NoError(t, store.WriteSnapshot(capturedAt, sampleCombinedData(42)))
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
		require.NoError(t, store.WriteSnapshot(capturedAt, sampleCombinedData(float64((i+1)*10))))
	}
	oldCapturedAt := now.Add(-2 * time.Hour).UnixMilli()
	require.NoError(t, store.WriteSnapshot(oldCapturedAt, sampleCombinedData(5)))

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
	data := sampleCombinedData(42)
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
