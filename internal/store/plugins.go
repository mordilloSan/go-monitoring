package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/mordilloSan/go-monitoring/internal/domain/container"
	modelnet "github.com/mordilloSan/go-monitoring/internal/domain/network"
	procmodel "github.com/mordilloSan/go-monitoring/internal/domain/process"
	"github.com/mordilloSan/go-monitoring/internal/domain/system"
)

const (
	PluginCPU         = "cpu"
	PluginMem         = "mem"
	PluginSwap        = "swap"
	PluginLoad        = "load"
	PluginDiskIO      = "diskio"
	PluginFS          = "fs"
	PluginNetwork     = "network"
	PluginGPU         = "gpu"
	PluginSensors     = "sensors"
	PluginContainers  = "containers"
	PluginSystemd     = "systemd"
	PluginProcesses   = "processes"
	PluginPrograms    = "programs"
	PluginConnections = "connections"
	PluginIRQ         = "irq"
	PluginSmart       = "smart"
)

var pluginNames = []string{
	PluginCPU,
	PluginMem,
	PluginSwap,
	PluginLoad,
	PluginDiskIO,
	PluginFS,
	PluginNetwork,
	PluginGPU,
	PluginSensors,
	PluginContainers,
	PluginSystemd,
	PluginProcesses,
	PluginPrograms,
	PluginConnections,
	PluginIRQ,
	PluginSmart,
}

var defaultHistoryPluginNames = []string{
	PluginCPU,
	PluginMem,
	PluginDiskIO,
	PluginNetwork,
	PluginContainers,
}

type CPUData struct {
	Cpu           float64           `json:"cpu_percent"`
	MaxCpu        float64           `json:"max_cpu_percent,omitempty"`
	CpuBreakdown  []float64         `json:"cpu_breakdown_percent,omitempty"`
	CpuCoresUsage system.Uint8Slice `json:"cpu_cores_percent,omitempty"`
}

type MemData struct {
	Mem          float64 `json:"memory_gb"`
	MaxMem       float64 `json:"max_memory_gb,omitempty"`
	MemUsed      float64 `json:"memory_used_gb"`
	MemPct       float64 `json:"memory_percent"`
	MemBuffCache float64 `json:"memory_buffer_cache_gb"`
	MemZfsArc    float64 `json:"memory_zfs_arc_gb,omitempty"`
}

type SwapData struct {
	Swap     float64 `json:"swap_gb,omitempty"`
	SwapUsed float64 `json:"swap_used_gb,omitempty"`
}

type LoadData struct {
	LoadAvg [3]float64 `json:"load_average,omitempty"`
	Battery [2]uint8   `json:"battery,omitzero"`
}

type DiskIOData struct {
	DiskTotal      float64    `json:"disk_total_gb"`
	DiskUsed       float64    `json:"disk_used_gb"`
	DiskPct        float64    `json:"disk_percent"`
	DiskReadPs     float64    `json:"disk_read_mb_per_second,omitzero"`
	DiskWritePs    float64    `json:"disk_write_mb_per_second,omitzero"`
	MaxDiskReadPs  float64    `json:"max_disk_read_mb_per_second,omitempty"`
	MaxDiskWritePs float64    `json:"max_disk_write_mb_per_second,omitempty"`
	DiskIO         [2]uint64  `json:"disk_io_bytes_per_second,omitzero"`
	MaxDiskIO      [2]uint64  `json:"max_disk_io_bytes_per_second,omitzero"`
	DiskIoStats    [6]float64 `json:"disk_io_stats,omitzero"`
	MaxDiskIoStats [6]float64 `json:"max_disk_io_stats,omitzero"`
}

type FSData struct {
	ExtraFs map[string]*system.FsStats `json:"extra_filesystems,omitempty"`
}

type NetworkData struct {
	NetworkSent       float64              `json:"network_sent_mb_per_second,omitzero"`
	NetworkRecv       float64              `json:"network_recv_mb_per_second,omitzero"`
	MaxNetworkSent    float64              `json:"max_network_sent_mb_per_second,omitempty"`
	MaxNetworkRecv    float64              `json:"max_network_recv_mb_per_second,omitempty"`
	Bandwidth         [2]uint64            `json:"bandwidth_bytes_per_second,omitzero"`
	MaxBandwidth      [2]uint64            `json:"max_bandwidth_bytes_per_second,omitzero"`
	NetworkInterfaces map[string][4]uint64 `json:"network_interfaces,omitempty"`
}

type SensorsData struct {
	Temperatures map[string]float64 `json:"temperatures,omitempty"`
}

type ProcessesData struct {
	Count *procmodel.Count    `json:"count,omitempty"`
	Items []procmodel.Process `json:"items"`
}

// PluginNames returns the canonical v1 plugin order.
func PluginNames() []string {
	return slices.Clone(pluginNames)
}

// DefaultHistoryPluginNames returns the default plugin history allowlist.
func DefaultHistoryPluginNames() []string {
	return slices.Clone(defaultHistoryPluginNames)
}

func IsPluginName(name string) bool {
	return slices.Contains(pluginNames, name)
}

func pluginCurrentTable(plugin string) string {
	return plugin + "_current"
}

func pluginHistoryTable(plugin string) string {
	return plugin + "_history"
}

func historyPluginSet(plugins []string) map[string]struct{} {
	out := make(map[string]struct{}, len(plugins))
	for _, plugin := range plugins {
		out[plugin] = struct{}{}
	}
	return out
}

func SnapshotPluginPayloads(data *system.CombinedData) map[string]any {
	return snapshotPluginPayloads(data)
}

func snapshotPluginPayloads(data *system.CombinedData) map[string]any {
	stats := data.Stats
	return map[string]any{
		PluginCPU: CPUData{
			Cpu:           stats.Cpu,
			MaxCpu:        stats.MaxCpu,
			CpuBreakdown:  stats.CpuBreakdown,
			CpuCoresUsage: stats.CpuCoresUsage,
		},
		PluginMem: MemData{
			Mem:          stats.Mem,
			MaxMem:       stats.MaxMem,
			MemUsed:      stats.MemUsed,
			MemPct:       stats.MemPct,
			MemBuffCache: stats.MemBuffCache,
			MemZfsArc:    stats.MemZfsArc,
		},
		PluginSwap: SwapData{
			Swap:     stats.Swap,
			SwapUsed: stats.SwapUsed,
		},
		PluginLoad: LoadData{
			LoadAvg: stats.LoadAvg,
			Battery: stats.Battery,
		},
		PluginDiskIO: DiskIOData{
			DiskTotal:      stats.DiskTotal,
			DiskUsed:       stats.DiskUsed,
			DiskPct:        stats.DiskPct,
			DiskReadPs:     stats.DiskReadPs,
			DiskWritePs:    stats.DiskWritePs,
			MaxDiskReadPs:  stats.MaxDiskReadPs,
			MaxDiskWritePs: stats.MaxDiskWritePs,
			DiskIO:         stats.DiskIO,
			MaxDiskIO:      stats.MaxDiskIO,
			DiskIoStats:    stats.DiskIoStats,
			MaxDiskIoStats: stats.MaxDiskIoStats,
		},
		PluginFS: FSData{
			ExtraFs: stats.ExtraFs,
		},
		PluginNetwork: NetworkData{
			NetworkSent:       stats.NetworkSent,
			NetworkRecv:       stats.NetworkRecv,
			MaxNetworkSent:    stats.MaxNetworkSent,
			MaxNetworkRecv:    stats.MaxNetworkRecv,
			Bandwidth:         stats.Bandwidth,
			MaxBandwidth:      stats.MaxBandwidth,
			NetworkInterfaces: stats.NetworkInterfaces,
		},
		PluginGPU:         nonNilMap(stats.GPUData),
		PluginSensors:     SensorsData{Temperatures: stats.Temperatures},
		PluginContainers:  containerCurrentRecords(data.Containers),
		PluginSystemd:     nonNilSlice(data.SystemdServices),
		PluginProcesses:   ProcessesData{Count: data.ProcessCount, Items: nonNilSlice(data.Processes)},
		PluginPrograms:    nonNilSlice(data.Programs),
		PluginConnections: nonNilPointer(data.Connections),
		PluginIRQ:         nonNilSlice(data.IRQs),
	}
}

func nonNilSlice[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

func nonNilMap[K comparable, V any](items map[K]V) map[K]V {
	if items == nil {
		return map[K]V{}
	}
	return items
}

func nonNilPointer[T any](item *T) any {
	if item == nil {
		return *new(T)
	}
	return item
}

func containerCurrentRecords(items []*container.Stats) []containerCurrentRecord {
	out := make([]containerCurrentRecord, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		out = append(out, containerCurrentRecord{
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
		})
	}
	return out
}

func parseHistoryPlugins(raw string, explicit bool, envValue func(string) (string, bool)) ([]string, error) {
	if !explicit {
		if value, ok := envValue("HISTORY"); ok {
			raw = value
			explicit = true
		}
	}
	if !explicit || strings.TrimSpace(raw) == "" {
		return DefaultHistoryPluginNames(), nil
	}

	normalized := strings.ToLower(strings.TrimSpace(raw))
	switch normalized {
	case "all":
		return PluginNames(), nil
	case "none":
		return nil, nil
	}

	seen := make(map[string]struct{})
	var out []string
	for part := range strings.SplitSeq(normalized, ",") {
		plugin := strings.TrimSpace(part)
		if plugin == "" {
			continue
		}
		if !IsPluginName(plugin) {
			return nil, fmt.Errorf("unknown history plugin %q", plugin)
		}
		if _, ok := seen[plugin]; ok {
			continue
		}
		seen[plugin] = struct{}{}
		out = append(out, plugin)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

//nolint:gocognit // Complexity comes from the number of plugin cases, not logical nesting.
func aggregatePluginHistoryJSON(plugin string, items []string) (string, error) {
	if len(items) == 0 {
		return "", errors.New("no history items to aggregate")
	}

	switch plugin {
	case PluginCPU:
		return averagePluginViaSystemStats(items, func(raw []byte, stats *system.Stats) error {
			var item CPUData
			if err := json.Unmarshal(raw, &item); err != nil {
				return err
			}
			stats.Cpu = item.Cpu
			stats.MaxCpu = item.MaxCpu
			stats.CpuBreakdown = item.CpuBreakdown
			stats.CpuCoresUsage = item.CpuCoresUsage
			return nil
		}, func(stats system.Stats) any {
			return snapshotPluginPayloads(&system.CombinedData{Stats: stats})[PluginCPU]
		})
	case PluginMem:
		return averagePluginViaSystemStats(items, func(raw []byte, stats *system.Stats) error {
			var item MemData
			if err := json.Unmarshal(raw, &item); err != nil {
				return err
			}
			stats.Mem = item.Mem
			stats.MaxMem = item.MaxMem
			stats.MemUsed = item.MemUsed
			stats.MemPct = item.MemPct
			stats.MemBuffCache = item.MemBuffCache
			stats.MemZfsArc = item.MemZfsArc
			return nil
		}, func(stats system.Stats) any {
			return snapshotPluginPayloads(&system.CombinedData{Stats: stats})[PluginMem]
		})
	case PluginSwap:
		return averagePluginViaSystemStats(items, func(raw []byte, stats *system.Stats) error {
			var item SwapData
			if err := json.Unmarshal(raw, &item); err != nil {
				return err
			}
			stats.Swap = item.Swap
			stats.SwapUsed = item.SwapUsed
			return nil
		}, func(stats system.Stats) any {
			return snapshotPluginPayloads(&system.CombinedData{Stats: stats})[PluginSwap]
		})
	case PluginLoad:
		return averagePluginViaSystemStats(items, func(raw []byte, stats *system.Stats) error {
			var item LoadData
			if err := json.Unmarshal(raw, &item); err != nil {
				return err
			}
			stats.LoadAvg = item.LoadAvg
			stats.Battery = item.Battery
			return nil
		}, func(stats system.Stats) any {
			return snapshotPluginPayloads(&system.CombinedData{Stats: stats})[PluginLoad]
		})
	case PluginDiskIO:
		return averagePluginViaSystemStats(items, func(raw []byte, stats *system.Stats) error {
			var item DiskIOData
			if err := json.Unmarshal(raw, &item); err != nil {
				return err
			}
			stats.DiskTotal = item.DiskTotal
			stats.DiskUsed = item.DiskUsed
			stats.DiskPct = item.DiskPct
			stats.DiskReadPs = item.DiskReadPs
			stats.DiskWritePs = item.DiskWritePs
			stats.MaxDiskReadPs = item.MaxDiskReadPs
			stats.MaxDiskWritePs = item.MaxDiskWritePs
			stats.DiskIO = item.DiskIO
			stats.MaxDiskIO = item.MaxDiskIO
			stats.DiskIoStats = item.DiskIoStats
			stats.MaxDiskIoStats = item.MaxDiskIoStats
			return nil
		}, func(stats system.Stats) any {
			return snapshotPluginPayloads(&system.CombinedData{Stats: stats})[PluginDiskIO]
		})
	case PluginFS:
		return averagePluginViaSystemStats(items, func(raw []byte, stats *system.Stats) error {
			var item FSData
			if err := json.Unmarshal(raw, &item); err != nil {
				return err
			}
			stats.ExtraFs = item.ExtraFs
			return nil
		}, func(stats system.Stats) any {
			return snapshotPluginPayloads(&system.CombinedData{Stats: stats})[PluginFS]
		})
	case PluginNetwork:
		return averagePluginViaSystemStats(items, func(raw []byte, stats *system.Stats) error {
			var item NetworkData
			if err := json.Unmarshal(raw, &item); err != nil {
				return err
			}
			stats.NetworkSent = item.NetworkSent
			stats.NetworkRecv = item.NetworkRecv
			stats.MaxNetworkSent = item.MaxNetworkSent
			stats.MaxNetworkRecv = item.MaxNetworkRecv
			stats.Bandwidth = item.Bandwidth
			stats.MaxBandwidth = item.MaxBandwidth
			stats.NetworkInterfaces = item.NetworkInterfaces
			return nil
		}, func(stats system.Stats) any {
			return snapshotPluginPayloads(&system.CombinedData{Stats: stats})[PluginNetwork]
		})
	case PluginGPU:
		return averagePluginViaSystemStats(items, func(raw []byte, stats *system.Stats) error {
			var item map[string]system.GPUData
			if err := json.Unmarshal(raw, &item); err != nil {
				return err
			}
			stats.GPUData = item
			return nil
		}, func(stats system.Stats) any {
			return stats.GPUData
		})
	case PluginSensors:
		return averagePluginViaSystemStats(items, func(raw []byte, stats *system.Stats) error {
			var item SensorsData
			if err := json.Unmarshal(raw, &item); err != nil {
				return err
			}
			stats.Temperatures = item.Temperatures
			return nil
		}, func(stats system.Stats) any {
			return snapshotPluginPayloads(&system.CombinedData{Stats: stats})[PluginSensors]
		})
	case PluginContainers:
		averaged, err := averageContainerStatsJSON(items)
		if err != nil {
			return "", err
		}
		return marshalJSON(averaged)
	default:
		return items[len(items)-1], nil
	}
}

func averagePluginViaSystemStats(
	items []string,
	fill func([]byte, *system.Stats) error,
	extract func(system.Stats) any,
) (string, error) {
	systemStatsJSON := make([]string, 0, len(items))
	for _, raw := range items {
		stats := system.Stats{}
		if err := fill([]byte(raw), &stats); err != nil {
			return "", err
		}
		statsRaw, err := marshalJSON(stats)
		if err != nil {
			return "", err
		}
		systemStatsJSON = append(systemStatsJSON, statsRaw)
	}
	averaged, err := averageSystemStatsJSON(systemStatsJSON)
	if err != nil {
		return "", err
	}
	return marshalJSON(extract(*averaged))
}

//nolint:gocognit // Complexity comes from the number of plugin cases, not logical nesting.
func applyPluginPayload(plugin string, raw []byte, data *system.CombinedData) error {
	switch plugin {
	case PluginCPU:
		var item CPUData
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		data.Stats.Cpu = item.Cpu
		data.Stats.MaxCpu = item.MaxCpu
		data.Stats.CpuBreakdown = item.CpuBreakdown
		data.Stats.CpuCoresUsage = item.CpuCoresUsage
	case PluginMem:
		var item MemData
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		data.Stats.Mem = item.Mem
		data.Stats.MaxMem = item.MaxMem
		data.Stats.MemUsed = item.MemUsed
		data.Stats.MemPct = item.MemPct
		data.Stats.MemBuffCache = item.MemBuffCache
		data.Stats.MemZfsArc = item.MemZfsArc
	case PluginSwap:
		var item SwapData
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		data.Stats.Swap = item.Swap
		data.Stats.SwapUsed = item.SwapUsed
	case PluginLoad:
		var item LoadData
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		data.Stats.LoadAvg = item.LoadAvg
		data.Stats.Battery = item.Battery
	case PluginDiskIO:
		var item DiskIOData
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		data.Stats.DiskTotal = item.DiskTotal
		data.Stats.DiskUsed = item.DiskUsed
		data.Stats.DiskPct = item.DiskPct
		data.Stats.DiskReadPs = item.DiskReadPs
		data.Stats.DiskWritePs = item.DiskWritePs
		data.Stats.MaxDiskReadPs = item.MaxDiskReadPs
		data.Stats.MaxDiskWritePs = item.MaxDiskWritePs
		data.Stats.DiskIO = item.DiskIO
		data.Stats.MaxDiskIO = item.MaxDiskIO
		data.Stats.DiskIoStats = item.DiskIoStats
		data.Stats.MaxDiskIoStats = item.MaxDiskIoStats
	case PluginFS:
		var item FSData
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		data.Stats.ExtraFs = item.ExtraFs
	case PluginNetwork:
		var item NetworkData
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		data.Stats.NetworkSent = item.NetworkSent
		data.Stats.NetworkRecv = item.NetworkRecv
		data.Stats.MaxNetworkSent = item.MaxNetworkSent
		data.Stats.MaxNetworkRecv = item.MaxNetworkRecv
		data.Stats.Bandwidth = item.Bandwidth
		data.Stats.MaxBandwidth = item.MaxBandwidth
		data.Stats.NetworkInterfaces = item.NetworkInterfaces
	case PluginGPU:
		if err := json.Unmarshal(raw, &data.Stats.GPUData); err != nil {
			return err
		}
	case PluginSensors:
		var item SensorsData
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		data.Stats.Temperatures = item.Temperatures
	case PluginContainers:
		return json.Unmarshal(raw, &data.Containers)
	case PluginSystemd:
		return json.Unmarshal(raw, &data.SystemdServices)
	case PluginProcesses:
		var item ProcessesData
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		data.ProcessCount = item.Count
		data.Processes = item.Items
	case PluginPrograms:
		return json.Unmarshal(raw, &data.Programs)
	case PluginConnections:
		if string(raw) == "null" {
			return nil
		}
		data.Connections = &modelnet.ConnectionStats{}
		return json.Unmarshal(raw, data.Connections)
	case PluginIRQ:
		return json.Unmarshal(raw, &data.IRQs)
	}
	return nil
}
