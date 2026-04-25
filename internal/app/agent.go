// Package app runs the standalone monitoring agent, collecting local system
// metrics, storing them in SQLite, and serving them over the built-in HTTP API.
package app

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	httpapi "github.com/mordilloSan/go-monitoring/internal/api/http"
	"github.com/mordilloSan/go-monitoring/internal/common"
	"github.com/mordilloSan/go-monitoring/internal/domain/system"
	dockerintegration "github.com/mordilloSan/go-monitoring/internal/integration/docker"
	"github.com/mordilloSan/go-monitoring/internal/store"
	"github.com/mordilloSan/go-monitoring/internal/utils"
	"github.com/mordilloSan/go-monitoring/internal/version"
)

const defaultDataCacheTimeMs uint16 = 60_000

type App struct {
	sync.Mutex                                   // Used to lock agent while collecting data
	debug             bool                       // true if LOG_LEVEL is set to debug
	memCalc           string                     // Memory calculation formula
	fsManager         *fsManager                 // Manages filesystem and disk I/O state
	networkManager    *networkManager            // Manages network interface and bandwidth state
	dockerManager     *dockerintegration.Manager // Manages Docker API requests
	sensorConfig      *SensorConfig              // Sensors config
	systemInfoManager *systemInfoManager         // Manages host info, details, and ZFS capability
	gpuManager        *GPUManager                // Manages GPU data
	cache             *systemDataCache           // Cache for system stats based on cache time
	dataDir           string                     // Directory for persisting data
	smartManager      *SmartManager              // Manages SMART data
	systemdManager    *systemdManager            // Manages systemd services
	connectionType    system.ConnectionType      // Connection type reported in summaries
	processManager    *processManager            // Manages per-process CPU state
	requestLogging    bool                       // Whether HTTP API requests are logged
	store             *store.Store               // Persistent local store
	httpRuntime       *httpRuntime               // HTTP server + effective listen address (nil before Start)
}

// New creates a new app with the given data directory for persisting data.
// If the data directory is not set, it will attempt to find the optimal directory.
func New(dataDir ...string) (app *App, err error) {
	app = &App{
		fsManager:         newFsManager(),
		networkManager:    newNetworkManager(),
		processManager:    newProcessManager(),
		systemInfoManager: newSystemInfoManager(),
		cache:             NewSystemDataCache(),
	}

	app.dataDir, err = store.GetDataDir(dataDir...)
	if err != nil {
		slog.Warn("Data directory not found")
	} else {
		slog.Info("Data directory", "path", app.dataDir)
	}

	app.memCalc, _ = utils.GetEnv("MEM_CALC")
	app.requestLogging = httpapi.RequestLoggingEnabled()
	app.sensorConfig = app.newSensorConfig()

	// Set up slog with a log level determined by the LOG_LEVEL env var
	if logLevelStr, exists := utils.GetEnv("LOG_LEVEL"); exists {
		switch strings.ToLower(logLevelStr) {
		case "debug":
			app.debug = true
			slog.SetLogLoggerLevel(slog.LevelDebug)
		case "warn":
			slog.SetLogLoggerLevel(slog.LevelWarn)
		case "error":
			slog.SetLogLoggerLevel(slog.LevelError)
		}
	}

	slog.Debug(version.Version)

	// initialize docker manager
	app.dockerManager = dockerintegration.NewManager(func() {
		app.systemInfoManager.updateSystemDetails(func(details *system.Details) {
			details.Podman = true
		})
	})

	// initialize system info
	app.systemInfoManager.refreshSystemDetails(app.dockerManager)

	// initialize disk info
	app.fsManager.initializeDiskInfo()

	// initialize net io stats
	app.networkManager.initializeNetIoStats()

	app.systemdManager, err = newSystemdManager()
	if err != nil {
		slog.Debug("Systemd", "err", err)
	}

	app.smartManager, err = NewSmartManager()
	if err != nil {
		slog.Debug("SMART", "err", err)
	}

	// SMART_INTERVAL env var overrides the SmartManager default refresh cadence.
	// SmartManager may be nil when smartctl is unavailable; the value is still
	// reported in systemDetails for diagnostic visibility.
	if smartIntervalEnv, exists := utils.GetEnv("SMART_INTERVAL"); exists {
		if duration, parseErr := time.ParseDuration(smartIntervalEnv); parseErr == nil && duration > 0 {
			app.systemInfoManager.systemDetails.SmartInterval = duration
			if app.smartManager != nil {
				app.smartManager.refreshInterval = duration
			}
			slog.Info("SMART_INTERVAL", "duration", duration)
		} else {
			slog.Warn("Invalid SMART_INTERVAL", "err", parseErr)
		}
	}

	// initialize GPU manager
	app.gpuManager, err = NewGPUManager()
	if err != nil {
		slog.Debug("GPU", "err", err)
	}

	// if debugging, print stats
	if app.debug {
		slog.Debug("Stats", "data", app.gatherStats(common.DataRequestOptions{CacheTimeMs: defaultDataCacheTimeMs, IncludeDetails: true}))
	}

	return app, nil
}

func (a *App) gatherStats(options common.DataRequestOptions) *system.CombinedData {
	a.Lock()
	defer a.Unlock()

	cacheTimeMs := options.CacheTimeMs
	data, isCached := a.cache.Get(cacheTimeMs)
	if isCached {
		slog.Debug("Cached data", "cacheTimeMs", cacheTimeMs)
		return data
	}

	*data = system.CombinedData{
		Stats: a.getSystemStats(cacheTimeMs),
		Info:  a.systemInfoManager.systemInfo,
	}

	// slog.Info("System data", "data", data, "cacheTimeMs", cacheTimeMs)

	if a.dockerManager != nil {
		if containerStats, err := a.dockerManager.GetStats(cacheTimeMs); err == nil {
			data.Containers = containerStats
			slog.Debug("Containers", "data", data.Containers)
		} else {
			slog.Debug("Containers", "err", err)
		}
	}

	// skip updating systemd services if cache time is not the default 60sec interval
	if a.systemdManager != nil && cacheTimeMs == defaultDataCacheTimeMs {
		services := a.systemdManager.getServiceStats(a.systemdManager.context(), nil, true)
		totalCount := uint16(len(services))
		if totalCount > 0 {
			numFailed := a.systemdManager.getFailedServiceCount()
			data.Info.Services = []uint16{totalCount, numFailed}
		}
		data.SystemdServices = services
	}

	if cacheTimeMs == defaultDataCacheTimeMs {
		data.ProcessCount, data.Processes, data.Programs = a.processManager.collectProcessStats()
		data.Connections = collectConnectionStats()
		data.IRQs = collectIRQStats()
	}

	data.Stats.ExtraFs = make(map[string]*system.FsStats)
	data.Info.ExtraFsPct = make(map[string]float64)
	for name, stats := range a.fsManager.fsStats {
		if !stats.Root && stats.DiskTotal > 0 {
			// Use custom name if available, otherwise use device name
			key := name
			if stats.Name != "" {
				key = stats.Name
			}
			data.Stats.ExtraFs[key] = stats
			// Add percentages to Info struct for dashboard
			if stats.DiskTotal > 0 {
				pct := utils.TwoDecimals((stats.DiskUsed / stats.DiskTotal) * 100)
				data.Info.ExtraFsPct[key] = pct
			}
		}
	}
	slog.Debug("Extra FS", "data", data.Stats.ExtraFs)

	a.cache.Set(cacheableStatsData(data), cacheTimeMs)

	return a.systemInfoManager.attachSystemDetails(data, cacheTimeMs, options.IncludeDetails)
}

func cacheableStatsData(data *system.CombinedData) *system.CombinedData {
	if data == nil {
		return nil
	}
	cached := *data
	cached.ProcessCount = nil
	cached.Processes = nil
	cached.Programs = nil
	cached.Connections = nil
	cached.IRQs = nil
	return &cached
}
