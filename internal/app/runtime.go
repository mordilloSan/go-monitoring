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
	"github.com/mordilloSan/go-monitoring/internal/common"
	"github.com/mordilloSan/go-monitoring/internal/domain/smart"
	"github.com/mordilloSan/go-monitoring/internal/health"
	"github.com/mordilloSan/go-monitoring/internal/store"
)

const defaultCollectorInterval = time.Minute

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
}

func (a *App) Start(opts RunOptions) error {
	sigCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	return a.StartContext(sigCtx, opts)
}

func (a *App) StartContext(ctx context.Context, opts RunOptions) error {
	if a.dataDir == "" {
		return errors.New("data directory not configured")
	}
	if a.store != nil {
		return errors.New("agent already started")
	}
	if opts.CollectorInterval <= 0 {
		opts.CollectorInterval = defaultCollectorInterval
	}
	if opts.Addr == "" {
		return errors.New("listen address is required")
	}

	historyPlugins, err := store.ParseHistoryPlugins(opts.History, opts.HistorySet)
	if err != nil {
		return err
	}

	persistentStore, err := store.OpenStore(a.dataDir, store.Options{HistoryPlugins: historyPlugins})
	if err != nil {
		return err
	}
	a.store = persistentStore
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
			Handler:           a.apiServer().Handler(opts.CollectorInterval),
			ReadHeaderTimeout: 5 * time.Second,
		},
	}

	serverErr := make(chan error, 1)
	go func() {
		if err := a.httpRuntime.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	// The child context also stops the collector on the server-error path where
	// the caller's context may still be live.
	collectorStarted = true
	collectorWG.Go(func() {
		a.runCollector(runCtx, opts.CollectorInterval)
	})

	var runErr error
	select {
	case <-runCtx.Done():
		slog.Info("Shutting down standalone agent")
	case err := <-serverErr:
		if err != nil {
			runErr = err
		}
	}

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

func (a *App) runCollector(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
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

func (a *App) apiServer() *httpapi.Server {
	return httpapi.NewServer(httpapi.Options{
		Metrics:              a.store,
		SmartRefresher:       a,
		DataDir:              a.dataDir,
		ListenAddr:           a.ListenAddr,
		SmartRefreshInterval: a.smartRefreshIntervalString,
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
