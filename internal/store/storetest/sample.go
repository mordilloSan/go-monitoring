// Package storetest provides shared test fixtures for the store package and
// its consumers. Importing it from non-test code is unsupported.
package storetest

import (
	"github.com/mordilloSan/go-monitoring/internal/model/container"
	modelnet "github.com/mordilloSan/go-monitoring/internal/model/network"
	procmodel "github.com/mordilloSan/go-monitoring/internal/model/process"
	"github.com/mordilloSan/go-monitoring/internal/model/system"
	"github.com/mordilloSan/go-monitoring/internal/model/systemd"
)

// SampleCombinedData returns a CombinedData snapshot whose field values are
// derived from cpu so that tests can assert on specific JSON substrings.
func SampleCombinedData(cpu float64) *system.CombinedData {
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
