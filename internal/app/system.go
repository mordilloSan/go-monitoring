package app

import (
	"bufio"
	"errors"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/mordilloSan/go-monitoring/internal/domain/system"
	"github.com/mordilloSan/go-monitoring/internal/integration/docker/dockerapi"
	"github.com/mordilloSan/go-monitoring/internal/utils"
	"github.com/mordilloSan/go-monitoring/internal/version"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

// systemInfoManager owns dynamic host info, mostly-static host details, the dirty
// flag for incremental detail propagation, and the ZFS capability bit detected at
// startup.
type systemInfoManager struct {
	systemInfo    system.Info    // Host system info (dynamic)
	systemDetails system.Details // Host system details (static, once-per-connection)
	detailsDirty  bool           // Whether system details have changed and need to be resent
	zfs           bool           // true if system has arcstats
}

type containerRuntimeInfo interface {
	IsPodman() bool
	GetHostInfo() (dockerapi.HostInfo, error)
}

func newSystemInfoManager() *systemInfoManager {
	return &systemInfoManager{}
}

// Sets initial / non-changing values about the host system
func (m *systemInfoManager) refreshSystemDetails(dockerManager containerRuntimeInfo) {
	m.systemInfo.AgentVersion = version.Version

	// get host info from Docker if available
	var hostInfo dockerapi.HostInfo

	if dockerManager != nil {
		m.systemDetails.Podman = dockerManager.IsPodman()
		hostInfo, _ = dockerManager.GetHostInfo()
	}

	m.systemDetails.Hostname, _ = os.Hostname()
	if arch, err := host.KernelArch(); err == nil {
		m.systemDetails.Arch = arch
	} else {
		m.systemDetails.Arch = runtime.GOARCH
	}

	platform, _, _, _ := host.PlatformInformation()
	m.systemDetails.OsName = hostInfo.OperatingSystem
	if m.systemDetails.OsName == "" {
		if prettyName, err := getOsPrettyName(); err == nil {
			m.systemDetails.OsName = prettyName
		} else {
			m.systemDetails.OsName = platform
		}
	}
	m.systemDetails.Kernel = hostInfo.KernelVersion
	if m.systemDetails.Kernel == "" {
		m.systemDetails.Kernel, _ = host.KernelVersion()
	}

	// cpu model
	if info, err := cpu.Info(); err == nil && len(info) > 0 {
		m.systemDetails.CpuModel = info[0].ModelName
	}
	// cores / threads
	cores, _ := cpu.Counts(false)
	threads := hostInfo.NCPU
	if threads == 0 {
		threads, _ = cpu.Counts(true)
	}
	// in lxc, logical cores reflects container limits, so use that as cores if lower
	if threads > 0 && threads < cores {
		cores = threads
	}
	m.systemDetails.Cores = cores
	m.systemDetails.Threads = threads

	// total memory
	m.systemDetails.MemoryTotal = hostInfo.MemTotal
	if m.systemDetails.MemoryTotal == 0 {
		if v, err := mem.VirtualMemory(); err == nil {
			m.systemDetails.MemoryTotal = v.Total
		}
	}

	// zfs
	if _, err := ARCSize(); err != nil {
		slog.Debug("Not monitoring ZFS ARC", "err", err)
	} else {
		m.zfs = true
	}
}

// attachSystemDetails returns details only for fresh default-interval responses.
func (m *systemInfoManager) attachSystemDetails(data *system.CombinedData, cacheTimeMs uint16, includeRequested bool) *system.CombinedData {
	if cacheTimeMs != defaultDataCacheTimeMs || (!includeRequested && !m.detailsDirty) {
		return data
	}

	// copy data to avoid adding details to the original cached struct
	response := *data
	response.Details = &m.systemDetails
	m.detailsDirty = false
	return &response
}

// updateSystemDetails applies a mutation to the static details payload and marks
// it for inclusion on the next fresh default-interval response.
func (m *systemInfoManager) updateSystemDetails(updateFunc func(details *system.Details)) {
	updateFunc(&m.systemDetails)
	m.detailsDirty = true
}

// Returns current info, stats about the host system
//
//nolint:gocognit // System stats assembly coordinates multiple collectors and cache-aware delta paths.
func (a *App) getSystemStats(cacheTimeMs uint16) system.Stats {
	var systemStats system.Stats

	// battery
	if batteryPercent, batteryState, err := GetBatteryStats(); err == nil {
		systemStats.Battery[0] = batteryPercent
		systemStats.Battery[1] = batteryState
	}

	// cpu metrics
	cpuMetrics, err := getCpuMetrics(cacheTimeMs)
	if err == nil {
		systemStats.Cpu = utils.TwoDecimals(cpuMetrics.Total)
		systemStats.CpuBreakdown = []float64{
			utils.TwoDecimals(cpuMetrics.User),
			utils.TwoDecimals(cpuMetrics.System),
			utils.TwoDecimals(cpuMetrics.Iowait),
			utils.TwoDecimals(cpuMetrics.Steal),
			utils.TwoDecimals(cpuMetrics.Idle),
		}
	} else {
		slog.Error("Error getting cpu metrics", "err", err)
	}

	// per-core cpu usage
	if perCoreUsage, err := getPerCoreCpuUsage(cacheTimeMs); err == nil {
		systemStats.CpuCoresUsage = perCoreUsage
	}

	// load average
	if avgstat, err := load.Avg(); err == nil {
		systemStats.LoadAvg[0] = avgstat.Load1
		systemStats.LoadAvg[1] = avgstat.Load5
		systemStats.LoadAvg[2] = avgstat.Load15
		slog.Debug("Load average", "5m", avgstat.Load5, "15m", avgstat.Load15)
	} else {
		slog.Error("Error getting load average", "err", err)
	}

	// memory
	if v, err := mem.VirtualMemory(); err == nil {
		// swap
		systemStats.Swap = utils.BytesToGigabytes(v.SwapTotal)
		systemStats.SwapUsed = utils.BytesToGigabytes(v.SwapTotal - v.SwapFree - v.SwapCached)
		// cache + buffers value for default mem calculation
		// note: gopsutil automatically adds SReclaimable to v.Cached
		cacheBuff := v.Cached + v.Buffers - v.Shared
		if cacheBuff <= 0 {
			cacheBuff = max(v.Total-v.Free-v.Used, 0)
		}
		// htop memory calculation overrides (likely outdated as of mid 2025)
		if a.memCalc == "htop" {
			// cacheBuff = v.Cached + v.Buffers - v.Shared
			v.Used = v.Total - (v.Free + cacheBuff)
			v.UsedPercent = float64(v.Used) / float64(v.Total) * 100.0
		}
		// if a.memCalc == "legacy" {
		// 	v.Used = v.Total - v.Free - v.Buffers - v.Cached
		// 	cacheBuff = v.Total - v.Free - v.Used
		// 	v.UsedPercent = float64(v.Used) / float64(v.Total) * 100.0
		// }
		// subtract ZFS ARC size from used memory and add as its own category
		if a.systemInfoManager.zfs {
			if arcSize, _ := ARCSize(); arcSize > 0 && arcSize < v.Used {
				v.Used = v.Used - arcSize
				v.UsedPercent = float64(v.Used) / float64(v.Total) * 100.0
				systemStats.MemZfsArc = utils.BytesToGigabytes(arcSize)
			}
		}
		systemStats.Mem = utils.BytesToGigabytes(v.Total)
		systemStats.MemBuffCache = utils.BytesToGigabytes(cacheBuff)
		systemStats.MemUsed = utils.BytesToGigabytes(v.Used)
		systemStats.MemPct = utils.TwoDecimals(v.UsedPercent)
	}

	// disk usage
	a.fsManager.updateDiskUsage(&systemStats)

	// disk i/o (cache-aware per interval)
	a.fsManager.updateDiskIo(cacheTimeMs, &systemStats)

	// network stats (per cache interval)
	a.networkManager.updateNetworkStats(cacheTimeMs, &systemStats)

	// temperatures
	// TODO: maybe refactor to methods on systemStats
	a.updateTemperatures(&systemStats)

	// GPU data
	info := &a.systemInfoManager.systemInfo
	if a.gpuManager != nil {
		// reset high gpu percent
		info.GpuPct = 0
		// get current GPU data
		if gpuData := a.gpuManager.GetCurrentData(cacheTimeMs); len(gpuData) > 0 {
			systemStats.GPUData = gpuData

			// add temperatures
			if systemStats.Temperatures == nil {
				systemStats.Temperatures = make(map[string]float64, len(gpuData))
			}
			highestTemp := 0.0
			for _, gpu := range gpuData {
				if gpu.Temperature > 0 {
					systemStats.Temperatures[gpu.Name] = gpu.Temperature
					if a.sensorConfig.primarySensor == gpu.Name {
						info.DashboardTemp = gpu.Temperature
					}
					if gpu.Temperature > highestTemp {
						highestTemp = gpu.Temperature
					}
				}
				// update high gpu percent for dashboard
				info.GpuPct = max(info.GpuPct, gpu.Usage)
			}
			// use highest temp for dashboard temp if dashboard temp is unset
			if info.DashboardTemp == 0 {
				info.DashboardTemp = highestTemp
			}
		}
	}

	// update system info
	info.ConnectionType = a.connectionType
	info.Cpu = systemStats.Cpu
	info.LoadAvg = systemStats.LoadAvg
	info.MemPct = systemStats.MemPct
	info.DiskPct = systemStats.DiskPct
	info.Battery = systemStats.Battery
	info.Uptime, _ = host.Uptime()
	info.BandwidthBytes = systemStats.Bandwidth[0] + systemStats.Bandwidth[1]
	info.Threads = a.systemInfoManager.systemDetails.Threads

	return systemStats
}

// getOsPrettyName attempts to get the pretty OS name from /etc/os-release on Linux systems
func getOsPrettyName() (string, error) {
	file, err := os.Open("/etc/os-release")
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if after, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			value := after
			value = strings.Trim(value, `"`)
			return value, nil
		}
	}

	return "", errors.New("pretty name not found")
}
