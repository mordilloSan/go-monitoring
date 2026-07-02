package app

import (
	"errors"
	"testing"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func restoreCPUState(t *testing.T) {
	t.Helper()
	originalTimes := cpuTimes
	originalLast := lastCpuTimes
	originalPerCore := lastPerCoreCpuTimes
	t.Cleanup(func() {
		cpuTimes = originalTimes
		lastCpuTimes = originalLast
		lastPerCoreCpuTimes = originalPerCore
	})
}

func TestClampPercent(t *testing.T) {
	assert.InDelta(t, 0.0, clampPercent(-1), 0.0001)
	assert.InDelta(t, 42.5, clampPercent(42.5), 0.0001)
	assert.InDelta(t, 100.0, clampPercent(1000), 0.0001)
}

func TestGetAllBusy(t *testing.T) {
	total, busy := getAllBusy(cpu.TimesStat{
		User: 10, System: 5, Idle: 70, Nice: 3, Iowait: 7, Irq: 1, Softirq: 2, Steal: 2,
		Guest: 100, GuestNice: 100,
	})

	assert.InDelta(t, 100.0, total, 0.0001)
	assert.InDelta(t, 23.0, busy, 0.0001)
}

func TestCalculateBusy(t *testing.T) {
	tests := []struct {
		name string
		t1   cpu.TimesStat
		t2   cpu.TimesStat
		want float64
	}{
		{
			name: "normal delta",
			t1:   cpu.TimesStat{User: 10, System: 5, Idle: 85},
			t2:   cpu.TimesStat{User: 20, System: 10, Idle: 110},
			want: 37.5,
		},
		{
			name: "counter reset",
			t1:   cpu.TimesStat{User: 20, System: 10, Idle: 110},
			t2:   cpu.TimesStat{User: 10, System: 5, Idle: 85},
			want: 0,
		},
		{
			name: "busy time does not increase",
			t1:   cpu.TimesStat{User: 10, System: 5, Idle: 85},
			t2:   cpu.TimesStat{User: 10, System: 5, Idle: 100},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, calculateBusy(tt.t1, tt.t2), 0.0001)
		})
	}
}

func TestGetCpuMetricsUsesDefaultBaselineForNewCacheInterval(t *testing.T) {
	restoreCPUState(t)
	lastCpuTimes = map[uint16]cpu.TimesStat{
		60000: {User: 10, System: 5, Idle: 85},
	}
	lastPerCoreCpuTimes = map[uint16][]cpu.TimesStat{}
	cpuTimes = func(percpu bool) ([]cpu.TimesStat, error) {
		require.False(t, percpu)
		return []cpu.TimesStat{{User: 20, System: 10, Idle: 110}}, nil
	}

	metrics, err := getCpuMetrics(15000)

	require.NoError(t, err)
	assert.InDelta(t, 37.5, metrics.Total, 0.0001)
	assert.InDelta(t, 25.0, metrics.User, 0.0001)
	assert.InDelta(t, 12.5, metrics.System, 0.0001)
	assert.InDelta(t, 62.5, metrics.Idle, 0.0001)
	assert.Equal(t, cpu.TimesStat{User: 20, System: 10, Idle: 110}, lastCpuTimes[15000])
}

func TestGetCpuMetricsHandlesEmptySamplesAndErrors(t *testing.T) {
	restoreCPUState(t)
	lastCpuTimes = map[uint16]cpu.TimesStat{60000: {}}

	cpuTimes = func(bool) ([]cpu.TimesStat, error) {
		return nil, nil
	}
	metrics, err := getCpuMetrics(60000)
	require.NoError(t, err)
	assert.Equal(t, CpuMetrics{}, metrics)

	expectedErr := errors.New("cpu unavailable")
	cpuTimes = func(bool) ([]cpu.TimesStat, error) {
		return nil, expectedErr
	}
	_, err = getCpuMetrics(60000)
	assert.ErrorIs(t, err, expectedErr)
}

func TestGetPerCoreCpuUsageUsesDefaultBaseline(t *testing.T) {
	restoreCPUState(t)
	lastCpuTimes = map[uint16]cpu.TimesStat{}
	lastPerCoreCpuTimes = map[uint16][]cpu.TimesStat{
		60000: {
			{User: 10, System: 0, Idle: 90},
			{User: 20, System: 0, Idle: 80},
		},
	}
	cpuTimes = func(percpu bool) ([]cpu.TimesStat, error) {
		require.True(t, percpu)
		return []cpu.TimesStat{
			{User: 20, System: 0, Idle: 100},
			{User: 30, System: 0, Idle: 110},
		}, nil
	}

	usage, err := getPerCoreCpuUsage(15000)

	require.NoError(t, err)
	assert.Equal(t, []uint8{50, 25}, []uint8(usage))
}
