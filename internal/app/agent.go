// Package app runs the standalone monitoring agent, collecting local system
// metrics, storing them in SQLite, and serving them over the built-in HTTP API.
package app

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	httpapi "github.com/mordilloSan/go-monitoring/internal/api/http"
	"github.com/mordilloSan/go-monitoring/internal/common"
	"github.com/mordilloSan/go-monitoring/internal/domain/system"
	dockerintegration "github.com/mordilloSan/go-monitoring/internal/integration/docker"
	"github.com/mordilloSan/go-monitoring/internal/logging"
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
	liveMu            sync.Mutex                 // Protects live API response caches
	liveCache         map[string]liveCacheEntry  // Current API raw response cache by plugin/key
	liveTTLs          map[string]time.Duration   // Current API cache TTL by plugin/key
	runtimeMu         sync.RWMutex               // Protects mutable runtime config visible to API/reload
	collectorInterval time.Duration
	configPath        string
	configSource      string
	configVersion     int
	historyPlugins    []string
}

// New creates a new app with the given data directory for persisting data.
// If the data directory is not set, it will attempt to find the optimal directory.
func New(ctx context.Context, dataDir ...string) (app *App, err error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	app = &App{
		systemInfoManager: newSystemInfoManager(),
		cache:             NewSystemDataCache(),
		liveCache:         make(map[string]liveCacheEntry),
	}

	app.configureLogging()
	app.liveTTLs = liveCurrentTTLsFromEnv(utils.GetEnv)
	slog.Info("Starting go-monitoring", "version", version.Version)

	app.dataDir, err = store.GetDataDir(dataDir...)
	if err != nil {
		slog.Warn("Data directory not found")
	} else {
		slog.Info("Data directory", "path", app.dataDir)
	}
	if app.debug {
		slog.Debug("Debug logging enabled")
	}

	app.fsManager = newFsManager()
	app.networkManager = newNetworkManager()
	app.processManager = newProcessManager()
	app.memCalc, _ = utils.GetEnv("MEM_CALC")
	app.requestLogging = httpapi.RequestLoggingEnabled()
	app.sensorConfig = app.newSensorConfig()

	// initialize docker manager
	app.dockerManager = dockerintegration.NewManager(ctx, func() {
		app.systemInfoManager.updateSystemDetails(func(details *system.Details) {
			details.Podman = true
		})
	})

	// initialize system info
	app.systemInfoManager.refreshSystemDetails(ctx, app.dockerManager)

	// initialize disk info
	app.fsManager.initializeDiskInfo(ctx)

	// initialize net io stats
	app.networkManager.initializeNetIoStats(ctx)

	initializeCpuMetrics(ctx)

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

	return app, nil
}

func (app *App) configureLogging() {
	if logLevelStr, exists := utils.GetEnv("LOG_LEVEL"); exists {
		switch strings.ToLower(logLevelStr) {
		case "debug":
			app.debug = true
			logging.SetLevel(slog.LevelDebug)
		case "warn":
			logging.SetLevel(slog.LevelWarn)
		case "error":
			logging.SetLevel(slog.LevelError)
		}
	}
}

func (a *App) gatherStats(ctx context.Context, options common.DataRequestOptions) (*system.CombinedData, error) {
	a.Lock()
	defer a.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cacheTimeMs := options.CacheTimeMs
	data, isCached := a.cache.Get(cacheTimeMs)
	if isCached {
		slog.Debug("Cached data", "cacheTimeMs", cacheTimeMs)
		return data, nil
	}

	stats, err := a.getSystemStats(ctx, cacheTimeMs)
	if err != nil {
		return nil, err
	}
	*data = system.CombinedData{
		Stats: stats,
		Info:  a.systemInfoManager.systemInfo,
	}

	// slog.Info("System data", "data", data, "cacheTimeMs", cacheTimeMs)

	if err := a.attachContainerStats(ctx, cacheTimeMs, data); err != nil {
		return nil, err
	}
	if err := a.attachDefaultIntervalStats(ctx, cacheTimeMs, data); err != nil {
		return nil, err
	}
	a.attachFilesystemStats(data)
	slog.Debug("Extra FS", "count", len(data.Stats.ExtraFs))

	a.cache.Set(cacheableStatsData(data), cacheTimeMs)

	return a.systemInfoManager.attachSystemDetails(data, cacheTimeMs, options.IncludeDetails), nil
}

func (a *App) attachContainerStats(ctx context.Context, cacheTimeMs uint16, data *system.CombinedData) error {
	if a.dockerManager == nil {
		return nil
	}
	containerStats, err := a.dockerManager.GetStats(ctx, cacheTimeMs)
	if err == nil {
		data.Containers = containerStats
		slog.Debug("Containers", "count", len(data.Containers))
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	slog.Debug("Containers", "err", err)
	return nil
}

func (a *App) attachDefaultIntervalStats(ctx context.Context, cacheTimeMs uint16, data *system.CombinedData) error {
	if cacheTimeMs != defaultDataCacheTimeMs {
		return nil
	}
	if err := a.attachSystemdStats(ctx, data); err != nil {
		return err
	}
	return a.attachCurrentDetailStats(ctx, data)
}

func (a *App) attachSystemdStats(ctx context.Context, data *system.CombinedData) error {
	if a.systemdManager == nil {
		return nil
	}
	services, err := a.systemdManager.getServiceStats(ctx, nil, false)
	if err != nil {
		return err
	}
	totalCount := uint16(len(services))
	if totalCount > 0 {
		numFailed := a.systemdManager.getFailedServiceCount()
		data.Info.Services = []uint16{totalCount, numFailed}
	}
	data.SystemdServices = services
	return nil
}

func (a *App) attachCurrentDetailStats(ctx context.Context, data *system.CombinedData) error {
	var err error
	data.ProcessCount, data.Processes, data.Programs, err = a.processManager.collectProcessStats(ctx)
	if err != nil {
		return err
	}
	data.Connections, err = collectConnectionStats(ctx)
	if err != nil {
		return err
	}
	data.IRQs, err = collectIRQStats(ctx)
	return err
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
