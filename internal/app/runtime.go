package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	httpapi "github.com/mordilloSan/go-monitoring/internal/api/http"
	apimodel "github.com/mordilloSan/go-monitoring/internal/api/model"
	"github.com/mordilloSan/go-monitoring/internal/common"
	"github.com/mordilloSan/go-monitoring/internal/domain/smart"
	"github.com/mordilloSan/go-monitoring/internal/health"
	"github.com/mordilloSan/go-monitoring/internal/store"
)

const DefaultCollectorInterval = 15 * time.Second

// httpRuntime groups the HTTP server with its effective listen address. The
// effective address differs from the requested one when the caller asks for
// an ephemeral port (":0") and needs to discover what the kernel chose.
type httpRuntime struct {
	server     *http.Server
	listenAddr string
}

type RunOptions struct {
	Addr              string
	CollectorInterval time.Duration
	History           string
	HistorySet        bool
	ConfigPath        string
	ConfigSource      string
	ConfigVersion     int
	CacheTTL          map[string]time.Duration
	ReloadConfig      func() (ReloadOptions, error)
}

type ReloadOptions struct {
	CollectorInterval time.Duration
	History           string
	HistorySet        bool
	CacheTTL          map[string]time.Duration
	ConfigSource      string
	ConfigVersion     int
}

func (a *App) Start(opts RunOptions) error {
	sigCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	return a.StartContext(sigCtx, opts)
}

func (a *App) StartContext(ctx context.Context, opts RunOptions) error {
	if err := a.validateAndNormalizeOpts(&opts); err != nil {
		return err
	}

	historyPlugins, err := store.ParseHistoryPlugins(opts.History, opts.HistorySet)
	if err != nil {
		return err
	}
	a.setRuntimeConfig(opts.CollectorInterval, historyPlugins, opts.ConfigPath, opts.ConfigSource, opts.ConfigVersion)
	if opts.CacheTTL != nil {
		a.SetLiveCurrentTTLs(opts.CacheTTL)
	}

	persistentStore, err := store.OpenStore(a.dataDir, store.Options{HistoryPlugins: historyPlugins})
	if err != nil {
		return err
	}
	a.store = persistentStore
	slog.Info("Database ready", "path", persistentStore.Path())
	slog.Info("Collector configured",
		"interval", opts.CollectorInterval,
		"history", historyPlugins,
		"smart_interval", a.smartRefreshIntervalString(),
	)
	defer func() {
		_ = a.store.Close()
		a.store = nil
	}()

	runCtx, cancelRun := context.WithCancel(ctx)
	var collectorWG sync.WaitGroup
	collectorStarted := false

	a.startManagers(runCtx)
	defer a.stopManagers()
	defer func() {
		cancelRun()
		if collectorStarted {
			collectorWG.Wait()
		}
	}()

	if collectErr := a.collectAndPersist(time.Now().UTC()); collectErr != nil {
		return collectErr
	}

	listener, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return err
	}
	defer listener.Close()

	a.httpRuntime = &httpRuntime{
		listenAddr: listener.Addr().String(),
		server: &http.Server{
			Addr:              opts.Addr,
			Handler:           a.apiServer().Handler(a.CollectorInterval),
			ReadHeaderTimeout: 5 * time.Second,
		},
	}
	slog.Info("HTTP server starting", "addr", a.httpRuntime.listenAddr, "request_logging", a.requestLogging)

	serverErr := make(chan error, 1)
	go func() {
		if err := a.httpRuntime.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	// The child context also stops the collector on the server-error path where
	// the caller's context may still be live.
	collectorIntervalUpdates := make(chan time.Duration, 1)
	collectorStarted = true
	collectorWG.Go(func() {
		a.runCollector(runCtx, opts.CollectorInterval, collectorIntervalUpdates)
	})

	hupCh := make(chan os.Signal, 1)
	signal.Notify(hupCh, syscall.SIGHUP)
	defer signal.Stop(hupCh)

	runErr := a.runEventLoop(runCtx, hupCh, serverErr, opts.ReloadConfig, collectorIntervalUpdates)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.httpRuntime.server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := health.CleanUp(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return runErr
}

func (a *App) validateAndNormalizeOpts(opts *RunOptions) error {
	if a.dataDir == "" {
		return errors.New("data directory not configured")
	}
	if a.store != nil {
		return errors.New("agent already started")
	}
	if opts.CollectorInterval <= 0 {
		opts.CollectorInterval = DefaultCollectorInterval
	}
	if opts.Addr == "" {
		return errors.New("listen address is required")
	}
	return nil
}

func (a *App) runEventLoop(ctx context.Context, hupCh <-chan os.Signal, serverErr <-chan error, reloadFn func() (ReloadOptions, error), updates chan time.Duration) error {
	var runErr error
	running := true
	for running {
		select {
		case <-ctx.Done():
			slog.Info("Shutting down standalone agent")
			running = false
		case <-hupCh:
			a.handleSIGHUP(reloadFn, updates)
		case err := <-serverErr:
			if err != nil {
				runErr = err
			}
			running = false
		}
	}
	return runErr
}

func (a *App) handleSIGHUP(reloadFn func() (ReloadOptions, error), updates chan time.Duration) {
	if reloadFn == nil {
		slog.Info("SIGHUP received; no config reload hook configured")
		return
	}
	reloaded, err := reloadFn()
	if err != nil {
		slog.Error("Config reload failed", "err", err)
		return
	}
	interval, historyPlugins, err := a.applyReload(reloaded)
	if err != nil {
		slog.Error("Config reload failed", "err", err)
		return
	}
	select {
	case updates <- interval:
	default:
		<-updates
		updates <- interval
	}
	slog.Info("Config reloaded",
		"interval", interval,
		"history", historyPlugins,
		"source", reloaded.ConfigSource,
		"version", reloaded.ConfigVersion,
	)
}

func (a *App) startManagers(ctx context.Context) {
	if a.systemdManager != nil {
		a.systemdManager.Start(ctx)
	}
	if a.gpuManager != nil {
		if err := a.gpuManager.Start(ctx); err != nil {
			slog.Debug("GPU", "err", err)
			a.gpuManager.Stop()
		}
	}
}

func (a *App) stopManagers() {
	if a.gpuManager != nil {
		a.gpuManager.Stop()
	}
	if a.systemdManager != nil {
		a.systemdManager.Stop()
	}
}

func (a *App) runCollector(ctx context.Context, interval time.Duration, intervalUpdates <-chan time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case nextInterval := <-intervalUpdates:
			if nextInterval <= 0 {
				nextInterval = DefaultCollectorInterval
			}
			ticker.Reset(nextInterval)
		case tickTime := <-ticker.C:
			if err := a.collectAndPersist(tickTime.UTC()); err != nil {
				slog.Error("collector tick failed", "err", err)
			}
		}
	}
}

func (a *App) collectAndPersist(now time.Time) error {
	data := a.gatherStats(common.DataRequestOptions{
		CacheTimeMs:    defaultDataCacheTimeMs,
		IncludeDetails: true,
	})
	capturedAt := now.UnixMilli()

	if err := a.store.WriteSnapshot(capturedAt, data); err != nil {
		return err
	}
	if err := health.Update(); err != nil {
		return err
	}

	// SMART refresh failures are logged when the refresh state changes so
	// persistent probe failures do not emit the same warning every cycle.
	_ = a.refreshSmartIfDue(now)
	if err := a.store.RunMaintenance(now); err != nil {
		return fmt.Errorf("maintenance failed: %w", err)
	}
	// Collection builds large transient process/network snapshots; release them
	// after the minute-scale tick so the agent's RSS stays close to baseline.
	debug.FreeOSMemory()
	return nil
}

func (a *App) refreshSmartIfDue(now time.Time) error {
	if a.smartManager == nil {
		return nil
	}

	a.Lock()
	shouldRefresh := a.smartManager.lastRefresh.IsZero() || now.Sub(a.smartManager.lastRefresh) >= a.smartManager.refreshInterval
	a.Unlock()
	if !shouldRefresh {
		return nil
	}

	err := a.refreshSmart(now, false)
	a.logSmartRefreshResult(err)
	return err
}

func (a *App) RefreshSmartNow() error {
	err := a.refreshSmart(time.Now().UTC(), true)
	a.logSmartRefreshResult(err)
	return err
}

func (a *App) refreshSmart(now time.Time, forceScan bool) error {
	if a.smartManager == nil {
		return a.store.WriteSmartDevices(now.UnixMilli(), map[string]smart.SmartData{})
	}

	if err := a.smartManager.Refresh(forceScan); err != nil {
		return err
	}
	if err := a.store.WriteSmartDevices(now.UnixMilli(), a.smartManager.GetCurrentData()); err != nil {
		return err
	}

	a.Lock()
	a.smartManager.lastRefresh = now
	a.Unlock()
	return nil
}

func (a *App) ListenAddr() string {
	if a.httpRuntime == nil {
		return ""
	}
	return a.httpRuntime.listenAddr
}

func (a *App) CollectorInterval() time.Duration {
	a.runtimeMu.RLock()
	defer a.runtimeMu.RUnlock()
	if a.collectorInterval <= 0 {
		return DefaultCollectorInterval
	}
	return a.collectorInterval
}

func (a *App) setRuntimeConfig(interval time.Duration, historyPlugins []string, configPath, configSource string, configVersion int) {
	if interval <= 0 {
		interval = DefaultCollectorInterval
	}
	a.runtimeMu.Lock()
	defer a.runtimeMu.Unlock()
	a.collectorInterval = interval
	a.historyPlugins = append([]string(nil), historyPlugins...)
	a.configPath = configPath
	a.configSource = configSource
	a.configVersion = configVersion
}

func (a *App) applyReload(opts ReloadOptions) (time.Duration, []string, error) {
	if opts.CollectorInterval <= 0 {
		opts.CollectorInterval = DefaultCollectorInterval
	}
	historyPlugins, err := store.ParseHistoryPlugins(opts.History, opts.HistorySet)
	if err != nil {
		return 0, nil, err
	}
	if opts.CacheTTL != nil {
		a.SetLiveCurrentTTLs(opts.CacheTTL)
	}
	if a.store != nil {
		a.store.SetHistoryPlugins(historyPlugins)
	}
	a.runtimeMu.RLock()
	configPath := a.configPath
	a.runtimeMu.RUnlock()
	a.setRuntimeConfig(opts.CollectorInterval, historyPlugins, configPath, opts.ConfigSource, opts.ConfigVersion)
	return opts.CollectorInterval, historyPlugins, nil
}

func (a *App) configInfo() apimodel.ConfigMeta {
	a.runtimeMu.RLock()
	defer a.runtimeMu.RUnlock()
	cacheTTL := make(map[string]string, len(a.liveTTLs))
	for key, ttl := range a.liveTTLs {
		cacheTTL[key] = ttl.String()
	}
	return apimodel.ConfigMeta{
		Path:              a.configPath,
		Source:            a.configSource,
		Version:           a.configVersion,
		CollectorInterval: a.collectorInterval.String(),
		HistoryPlugins:    append([]string(nil), a.historyPlugins...),
		CacheTTL:          cacheTTL,
	}
}

func (a *App) apiServer() *httpapi.Server {
	return httpapi.NewServer(httpapi.Options{
		Metrics:              a.store,
		Current:              a,
		SmartRefresher:       a,
		DataDir:              a.dataDir,
		ListenAddr:           a.ListenAddr,
		SmartRefreshInterval: a.smartRefreshIntervalString,
		ConfigInfo:           a.configInfo,
		RequestLogging:       a.requestLogging,
	})
}

func (a *App) smartRefreshIntervalString() string {
	if a.smartManager == nil {
		return ""
	}
	return a.smartManager.refreshInterval.String()
}

func (a *App) logSmartRefreshResult(err error) {
	if a.smartManager == nil {
		return
	}
	currentError := ""
	if err != nil {
		currentError = err.Error()
	}

	a.Lock()
	previousError := a.smartManager.lastRefreshError
	a.smartManager.lastRefreshError = currentError
	a.Unlock()

	switch {
	case currentError == "":
		if previousError != "" {
			slog.Info("smart refresh recovered")
		}
	case previousError != currentError:
		slog.Warn("smart refresh failed", "err", err)
	default:
		slog.Debug("smart refresh still failing", "err", err)
	}
}
