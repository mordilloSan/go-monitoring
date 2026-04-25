package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/mordilloSan/go-monitoring/internal/common"
	"github.com/mordilloSan/go-monitoring/internal/health"
	"github.com/mordilloSan/go-monitoring/internal/model/smart"
)

const defaultCollectorInterval = time.Minute

type RunOptions struct {
	Addr              string
	CollectorInterval time.Duration
}

func (a *Agent) Start(opts RunOptions) error {
	sigCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	return a.StartContext(sigCtx, opts)
}

func (a *Agent) StartContext(ctx context.Context, opts RunOptions) error {
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

	store, err := OpenStore(a.dataDir)
	if err != nil {
		return err
	}
	a.store = store
	defer func() {
		_ = a.store.Close()
		a.store = nil
	}()

	if collectErr := a.collectAndPersist(time.Now().UTC()); collectErr != nil {
		return collectErr
	}

	listener, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return err
	}
	defer listener.Close()

	a.listenAddr = listener.Addr().String()
	a.httpServer = &http.Server{
		Addr:              opts.Addr,
		Handler:           a.routes(opts.CollectorInterval),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		if err := a.httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	// Derive a cancellable context for the collector so the server-error path
	// also stops it. Defers run LIFO: cancelRun fires first, then collectorWG.Wait
	// blocks the function return until runCollector exits, then the existing
	// listener.Close and store.Close defers run.
	runCtx, cancelRun := context.WithCancel(ctx)
	var collectorWG sync.WaitGroup
	collectorWG.Go(func() {
		a.runCollector(runCtx, opts.CollectorInterval)
	})
	defer collectorWG.Wait()
	defer cancelRun()

	var runErr error
	select {
	case <-ctx.Done():
		slog.Info("Shutting down standalone agent")
	case err := <-serverErr:
		if err != nil {
			runErr = err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.httpServer.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := health.CleanUp(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return runErr
}

func (a *Agent) runCollector(ctx context.Context, interval time.Duration) {
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

func (a *Agent) collectAndPersist(now time.Time) error {
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
	return nil
}

func (a *Agent) refreshSmartIfDue(now time.Time) error {
	if a.smartManager == nil {
		return nil
	}

	a.Lock()
	shouldRefresh := a.lastSmartRefresh.IsZero() || now.Sub(a.lastSmartRefresh) >= a.smartRefreshInterval
	a.Unlock()
	if !shouldRefresh {
		return nil
	}

	err := a.refreshSmart(now, false)
	a.logSmartRefreshResult(err)
	return err
}

func (a *Agent) RefreshSmartNow() error {
	err := a.refreshSmart(time.Now().UTC(), true)
	a.logSmartRefreshResult(err)
	return err
}

func (a *Agent) refreshSmart(now time.Time, forceScan bool) error {
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
	a.lastSmartRefresh = now
	a.Unlock()
	return nil
}

func (a *Agent) ListenAddr() string {
	return a.listenAddr
}

func (a *Agent) logSmartRefreshResult(err error) {
	currentError := ""
	if err != nil {
		currentError = err.Error()
	}

	a.Lock()
	previousError := a.lastSmartRefreshError
	a.lastSmartRefreshError = currentError
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
