package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	"github.com/mordilloSan/go-monitoring/internal/version"
)

const DefaultCollectorInterval = 15 * time.Second

// httpRuntime groups the HTTP server with its effective listen address. The
// effective address differs from the requested one when the caller asks for
// an ephemeral port (":0") and needs to discover what the kernel chose.
type httpRuntime struct {
	name             string
	requestedAddress string
	server           *http.Server
	effectiveAddress string
	apis             []string
}

type ListenerOptions struct {
	Name       string
	Address    string
	APIs       []string
	BestEffort bool
}

type RunOptions struct {
	Listeners         []ListenerOptions
	CollectorInterval time.Duration
	History           string
	HistorySet        bool
	ConfigPath        string
	ConfigSource      string
	ConfigVersion     int
	CacheTTL          map[string]time.Duration
	CommandExecutor   httpapi.CommandExecutor
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

	if collectErr := a.collectAndPersist(runCtx, time.Now().UTC()); collectErr != nil {
		return collectErr
	}

	serverErr := make(chan error, 1)
	if err := a.startHTTPServers(opts, serverErr); err != nil {
		if shutdownErr := a.shutdownHTTPServers(ctx); shutdownErr != nil {
			return errors.Join(err, shutdownErr)
		}
		return err
	}

	// The child context also stops the collector on the server-error path where
	// the caller's context may still be live.
	collectorIntervalUpdates := make(chan time.Duration, 1)
	a.runtimeMu.Lock()
	a.intervalUpdates = collectorIntervalUpdates
	a.runtimeMu.Unlock()
	defer func() {
		a.runtimeMu.Lock()
		a.intervalUpdates = nil
		a.runtimeMu.Unlock()
	}()
	collectorStarted = true
	collectorWG.Go(func() {
		a.runCollector(runCtx, opts.CollectorInterval, collectorIntervalUpdates)
	})

	hupCh := make(chan os.Signal, 1)
	signal.Notify(hupCh, syscall.SIGHUP)
	defer signal.Stop(hupCh)

	runErr := a.runEventLoop(runCtx, hupCh, serverErr, opts.ReloadConfig, collectorIntervalUpdates)

	if err := a.shutdownHTTPServers(ctx); err != nil {
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
	return nil
}

func (a *App) startHTTPServers(opts RunOptions, serverErr chan<- error) error {
	if len(opts.Listeners) == 0 {
		slog.Info("HTTP servers disabled")
		return nil
	}
	for _, listenerOpts := range opts.Listeners {
		if IsListenDisabled(listenerOpts.Address) {
			slog.Info("HTTP listener disabled", "name", listenerOpts.Name, "address", listenerOpts.Address)
			continue
		}
		listener, err := openListener(listenerOpts.Address)
		if err != nil {
			if listenerOpts.BestEffort {
				slog.Warn("HTTP listener unavailable; continuing", "name", listenerOpts.Name, "address", listenerOpts.Address, "err", err)
				continue
			}
			return err
		}
		runtime := &httpRuntime{
			name:             listenerOpts.Name,
			requestedAddress: listenerOpts.Address,
			effectiveAddress: listener.Addr().String(),
			apis:             append([]string(nil), listenerOpts.APIs...),
			server: &http.Server{
				Addr:              listenerOpts.Address,
				Handler:           a.apiServer(opts.CommandExecutor).HandlerFor(a.CollectorInterval, listenerOpts.APIs),
				ReadHeaderTimeout: 5 * time.Second,
			},
		}
		a.httpRuntimes = append(a.httpRuntimes, runtime)
		slog.Info("HTTP server starting",
			"name", runtime.name,
			"address", runtime.effectiveAddress,
			"apis", runtime.apis,
			"request_logging", a.requestLogging,
		)
		go func(server *http.Server) {
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serverErr <- err
			}
		}(runtime.server)
	}
	return nil
}

func (a *App) shutdownHTTPServers(ctx context.Context) error {
	var shutdownErr error
	for _, runtime := range a.httpRuntimes {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		if err := runtime.server.Shutdown(shutdownCtx); err != nil {
			shutdownErr = errors.Join(shutdownErr, err)
		}
		cancel()
	}
	a.httpRuntimes = nil
	return shutdownErr
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

func (a *App) ReloadRuntime(opts ReloadOptions) error {
	interval, historyPlugins, err := a.applyReload(opts)
	if err != nil {
		return err
	}
	a.runtimeMu.RLock()
	updates := a.intervalUpdates
	a.runtimeMu.RUnlock()
	if updates != nil {
		select {
		case updates <- interval:
		default:
			<-updates
			updates <- interval
		}
	}
	slog.Info("Config reloaded",
		"interval", interval,
		"history", historyPlugins,
		"source", opts.ConfigSource,
		"version", opts.ConfigVersion,
	)
	return nil
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
			if err := a.collectAndPersist(ctx, tickTime.UTC()); err != nil {
				slog.Error("collector tick failed", "err", err)
			}
		}
	}
}

func (a *App) collectAndPersist(ctx context.Context, now time.Time) error {
	data, err := a.gatherStats(ctx, common.DataRequestOptions{
		CacheTimeMs:    defaultDataCacheTimeMs,
		IncludeDetails: true,
	})
	if err != nil {
		return err
	}
	capturedAt := now.UnixMilli()

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.store.WriteSnapshot(capturedAt, data); err != nil {
		return err
	}
	if err := health.Update(); err != nil {
		return err
	}

	// SMART refresh failures are logged when the refresh state changes so
	// persistent probe failures do not emit the same warning every cycle.
	_ = a.refreshSmartIfDue(ctx, now)
	if err := a.store.RunMaintenance(now); err != nil {
		return fmt.Errorf("maintenance failed: %w", err)
	}
	// Collection builds large transient process/network snapshots; release them
	// after the minute-scale tick so the agent's RSS stays close to baseline.
	debug.FreeOSMemory()
	return nil
}

func (a *App) refreshSmartIfDue(ctx context.Context, now time.Time) error {
	if a.smartManager == nil {
		return nil
	}

	a.Lock()
	shouldRefresh := a.smartManager.lastRefresh.IsZero() || now.Sub(a.smartManager.lastRefresh) >= a.smartManager.refreshInterval
	a.Unlock()
	if !shouldRefresh {
		return nil
	}

	err := a.refreshSmart(ctx, now, false)
	a.logSmartRefreshResult(err)
	return err
}

func (a *App) RefreshSmartNow(ctx context.Context) error {
	err := a.refreshSmart(ctx, time.Now().UTC(), true)
	a.logSmartRefreshResult(err)
	return err
}

func (a *App) refreshSmart(ctx context.Context, now time.Time, forceScan bool) error {
	if a.smartManager == nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		return a.store.WriteSmartDevices(now.UnixMilli(), map[string]smart.SmartData{})
	}

	if err := a.smartManager.Refresh(ctx, forceScan); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
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

func (a *App) Listeners() []apimodel.ListenerMeta {
	out := make([]apimodel.ListenerMeta, 0, len(a.httpRuntimes))
	for _, runtime := range a.httpRuntimes {
		out = append(out, runtime.meta(true))
	}
	return out
}

func (r *httpRuntime) meta(active bool) apimodel.ListenerMeta {
	return apimodel.ListenerMeta{
		Name:             r.name,
		Address:          r.requestedAddress,
		EffectiveAddress: r.effectiveAddress,
		APIs:             append([]string(nil), r.apis...),
		Active:           active,
	}
}

func (a *App) StatusMeta() apimodel.MetaResponse {
	dbPath := ""
	if a.store != nil {
		dbPath = a.store.Path()
	}
	return apimodel.MetaResponse{
		Version:              version.Version,
		DataDir:              a.dataDir,
		DBPath:               dbPath,
		Listeners:            a.Listeners(),
		CollectorInterval:    a.CollectorInterval().String(),
		SmartRefreshInterval: a.smartRefreshIntervalString(),
		Config:               a.configInfo(),
		Retention:            store.RetentionStrings(),
	}
}

func (a *App) CheckDatabase() error {
	if a.store == nil {
		return errors.New("database not open")
	}
	return a.store.IntegrityCheck()
}

func (a *App) MaintainDatabase(ctx context.Context) error {
	if a.store == nil {
		return errors.New("database not open")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.store.RunMaintenance(time.Now().UTC()); err != nil {
		return err
	}
	return a.store.IntegrityCheck()
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

func (a *App) apiServer(commandExecutor httpapi.CommandExecutor) *httpapi.Server {
	return httpapi.NewServer(httpapi.Options{
		Metrics:              a.store,
		Current:              a,
		SmartRefresher:       a,
		CommandExecutor:      commandExecutor,
		DataDir:              a.dataDir,
		Listeners:            a.Listeners,
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
