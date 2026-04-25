package app

import (
	"testing"

	procmodel "github.com/mordilloSan/go-monitoring/internal/domain/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGroupProgramStats(t *testing.T) {
	processes := []procmodel.Process{
		{PID: 2, Name: "nginx", CPUPercent: 1.2, MemoryPercent: 0.5, MemoryInfo: procmodel.MemoryInfo{RSS: 100}},
		{PID: 1, Name: "nginx", CPUPercent: 2.3, MemoryPercent: 0.7, MemoryInfo: procmodel.MemoryInfo{RSS: 200}},
		{PID: 3, Name: "postgres", CPUPercent: 0.4, MemoryPercent: 2, MemoryInfo: procmodel.MemoryInfo{RSS: 300}},
	}

	programs := groupProgramStats(processes)

	require.Len(t, programs, 2)
	assert.Equal(t, "nginx", programs[0].Name)
	assert.Equal(t, 2, programs[0].Count)
	assert.Equal(t, 3.5, programs[0].CPUPercent)
	assert.Equal(t, 1.2, programs[0].MemoryPercent)
	assert.Equal(t, uint64(300), programs[0].MemoryRSSBytes)
	assert.Equal(t, []int32{1, 2}, programs[0].PIDs)
}

func TestUpdateProcessCount(t *testing.T) {
	var count procmodel.Count

	updateProcessCount(&count, []string{"running"})
	updateProcessCount(&count, []string{"sleep"})
	updateProcessCount(&count, []string{"zombie"})
	updateProcessCount(&count, []string{"blocked"})

	assert.Equal(t, 1, count.Running)
	assert.Equal(t, 1, count.Sleeping)
	assert.Equal(t, 1, count.Zombie)
	assert.Equal(t, 1, count.Blocked)
}
