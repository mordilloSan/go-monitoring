package systemd_test

import (
	"runtime"
	"testing"
	"time"

	"github.com/mordilloSan/go-monitoring/internal/model/systemd"
	"github.com/stretchr/testify/assert"
)

func TestParseServiceStatus(t *testing.T) {
	tests := []struct {
		input    string
		expected systemd.ServiceState
	}{
		{"active", systemd.StatusActive},
		{"inactive", systemd.StatusInactive},
		{"failed", systemd.StatusFailed},
		{"activating", systemd.StatusActivating},
		{"deactivating", systemd.StatusDeactivating},
		{"reloading", systemd.StatusReloading},
		{"unknown", systemd.StatusInactive}, // default case
		{"", systemd.StatusInactive},        // default case
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			result := systemd.ParseServiceStatus(test.input)
			assert.Equal(t, test.expected, result)
		})
	}
}

func TestParseServiceSubState(t *testing.T) {
	tests := []struct {
		input    string
		expected systemd.ServiceSubState
	}{
		{"dead", systemd.SubStateDead},
		{"running", systemd.SubStateRunning},
		{"exited", systemd.SubStateExited},
		{"failed", systemd.SubStateFailed},
		{"unknown", systemd.SubStateUnknown},
		{"other", systemd.SubStateUnknown}, // default case
		{"", systemd.SubStateUnknown},      // default case
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			result := systemd.ParseServiceSubState(test.input)
			assert.Equal(t, test.expected, result)
		})
	}
}

func TestServiceUpdateCPUPercentInitializesBaseline(t *testing.T) {
	t.Run("initial call sets CPU to 0", func(t *testing.T) {
		service := &systemd.Service{}
		service.UpdateCPUPercent(1000)
		assert.Equal(t, 0.0, service.Cpu)
		assert.Equal(t, uint64(1000), service.PrevCpuUsage)
		assert.False(t, service.PrevReadTime.IsZero())
	})
}

func TestServiceUpdateCPUPercentCalculatesUsageAcrossAvailableCPUs(t *testing.T) {
	service := &systemd.Service{
		PrevCpuUsage: 1_000,
		PrevReadTime: time.Now().Add(-time.Second),
	}

	halfCoreSecondPerCPU := uint64(runtime.NumCPU()) * uint64(500*time.Millisecond)
	service.UpdateCPUPercent(service.PrevCpuUsage + halfCoreSecondPerCPU)

	assert.InDelta(t, 50.0, service.Cpu, 1.0)
	assert.Equal(t, uint64(1_000)+halfCoreSecondPerCPU, service.PrevCpuUsage)
	assert.Equal(t, service.Cpu, service.CpuPeak)
}

func TestServiceUpdateCPUPercentKeepsHigherPeak(t *testing.T) {
	service := &systemd.Service{
		CpuPeak:      80,
		PrevCpuUsage: 1_000,
		PrevReadTime: time.Now().Add(-time.Second),
	}

	tenthCoreSecondPerCPU := uint64(runtime.NumCPU()) * uint64(100*time.Millisecond)
	service.UpdateCPUPercent(service.PrevCpuUsage + tenthCoreSecondPerCPU)

	assert.InDelta(t, 10.0, service.Cpu, 1.0)
	assert.Equal(t, 80.0, service.CpuPeak)
}

func TestServiceUpdateCPUPercentHandlesNonIncreasingClock(t *testing.T) {
	service := &systemd.Service{
		Cpu:          12.5,
		PrevCpuUsage: 1_000,
		PrevReadTime: time.Now().Add(time.Second),
	}

	service.UpdateCPUPercent(2_000)

	assert.Equal(t, 12.5, service.Cpu)
	assert.Equal(t, uint64(2_000), service.PrevCpuUsage)
	assert.False(t, service.PrevReadTime.IsZero())
}

func TestServiceUpdateCPUPercentHandlesCPUUsageReset(t *testing.T) {
	service := &systemd.Service{
		Cpu:          42,
		PrevCpuUsage: 1_000,
		PrevReadTime: time.Now().Add(-time.Second),
	}

	service.UpdateCPUPercent(500)

	assert.Equal(t, 0.0, service.Cpu)
	assert.Equal(t, uint64(500), service.PrevCpuUsage)
}
