//go:build linux

package agent

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/mordilloSan/go-monitoring/internal/model/systemd"
	"github.com/mordilloSan/go-monitoring/internal/utils"
)

var errNoActiveTime = errors.New("no active time")

// systemdManager manages the collection of systemd service statistics.
type systemdManager struct {
	sync.Mutex
	serviceStatsMap map[string]*systemd.Service
	isRunning       bool
	hasFreshStats   bool
	patterns        []string
	lifecycleMu     sync.Mutex
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
}

// isSystemdAvailable checks if systemd is used on the system to avoid unnecessary connection attempts (#1548)
func isSystemdAvailable() bool {
	paths := []string{
		"/run/systemd/system",
		"/run/dbus/system_bus_socket",
		"/var/run/dbus/system_bus_socket",
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	if data, err := os.ReadFile("/proc/1/comm"); err == nil {
		return strings.TrimSpace(string(data)) == "systemd"
	}
	return false
}

// newSystemdManager creates a new systemdManager.
func newSystemdManager() (*systemdManager, error) {
	if skipSystemd, _ := utils.GetEnv("SKIP_SYSTEMD"); skipSystemd == "true" {
		return nil, nil
	}

	// Check if systemd is available on the system before attempting connection
	if !isSystemdAvailable() {
		slog.Debug("Systemd not available")
		return nil, nil
	}

	manager := &systemdManager{
		serviceStatsMap: make(map[string]*systemd.Service),
		patterns:        getServicePatterns(),
	}

	return manager, nil
}

func (sm *systemdManager) Start(ctx context.Context) {
	if sm == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sm.lifecycleMu.Lock()
	if sm.cancel != nil {
		sm.lifecycleMu.Unlock()
		return
	}
	sm.ctx, sm.cancel = context.WithCancel(ctx)
	sm.lifecycleMu.Unlock()
	sm.startWorker(sm.ctx)
}

func (sm *systemdManager) Stop() {
	if sm == nil {
		return
	}
	sm.lifecycleMu.Lock()
	cancel := sm.cancel
	sm.cancel = nil
	sm.ctx = nil
	sm.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	sm.wg.Wait()
	sm.lifecycleMu.Lock()
	sm.isRunning = false
	sm.lifecycleMu.Unlock()
}

func (sm *systemdManager) context() context.Context {
	if sm.ctx != nil {
		return sm.ctx
	}
	return context.Background()
}

func (sm *systemdManager) startWorker(ctx context.Context) {
	if sm.isRunning {
		return
	}
	sm.isRunning = true
	// prime the service stats map with the current services
	_ = sm.getServiceStats(ctx, nil, true)
	// update the services every 10 minutes
	sm.wg.Go(func() {
		for {
			if !sleepUntilDone(ctx, time.Minute*10) {
				return
			}
			_ = sm.getServiceStats(ctx, nil, true)
		}
	})
}

// getFailedServiceCount returns the number of systemd services in a failed state.
func (sm *systemdManager) getFailedServiceCount() uint16 {
	sm.Lock()
	defer sm.Unlock()
	count := uint16(0)
	for _, service := range sm.serviceStatsMap {
		if service.State == systemd.StatusFailed {
			count++
		}
	}
	return count
}

// getServiceStats collects statistics for all running systemd services.
func (sm *systemdManager) getServiceStats(ctx context.Context, conn *dbus.Conn, refresh bool) []*systemd.Service {
	// start := time.Now()
	// defer func() {
	// 	slog.Info("systemdManager.getServiceStats", "duration", time.Since(start))
	// }()

	if ctx == nil {
		ctx = context.Background()
	}

	if !refresh {
		return sm.cachedServiceStats()
	}

	conn, closeConn, ok := sm.systemdConnection(ctx, conn)
	if !ok {
		return nil
	}
	if closeConn {
		defer conn.Close()
	}

	units, err := conn.ListUnitsByPatternsContext(ctx, []string{"loaded"}, sm.patterns)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		slog.Error("Error listing systemd service units", "err", err)
		return nil
	}

	services, currentUnits, skippedNeverActive, skippedErrors := sm.collectSystemdUnitStats(ctx, conn, units)
	sm.removeStaleServices(currentUnits)

	sm.hasFreshStats = true
	slog.Debug("systemd services collected",
		"units", len(units),
		"services", len(services),
		"skipped_never_active", skippedNeverActive,
		"skipped_errors", skippedErrors,
	)
	return services
}

func (sm *systemdManager) cachedServiceStats() []*systemd.Service {
	sm.Lock()
	defer sm.Unlock()

	services := make([]*systemd.Service, 0, len(sm.serviceStatsMap))
	for _, service := range sm.serviceStatsMap {
		services = append(services, service)
	}
	sm.hasFreshStats = false
	return services
}

func (sm *systemdManager) systemdConnection(ctx context.Context, conn *dbus.Conn) (*dbus.Conn, bool, bool) {
	if conn != nil && conn.Connected() {
		return conn, false, true
	}

	conn, err := dbus.NewSystemConnectionContext(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("systemd dbus connection failed", "err", err)
		}
		return nil, false, false
	}
	return conn, true, true
}

func (sm *systemdManager) collectSystemdUnitStats(ctx context.Context, conn *dbus.Conn, units []dbus.UnitStatus) ([]*systemd.Service, map[string]struct{}, int, int) {
	services := make([]*systemd.Service, 0, len(units))
	currentUnits := make(map[string]struct{}, len(units))
	skippedNeverActive := 0
	skippedErrors := 0

	for _, unit := range units {
		currentUnits[unit.Name] = struct{}{}
		service, err := sm.updateServiceStats(ctx, conn, unit)
		switch {
		case err == nil:
			services = append(services, service)
		case errors.Is(err, errNoActiveTime):
			skippedNeverActive++
		default:
			skippedErrors++
			slog.Debug("systemd service skipped", "unit", unit.Name, "err", err)
		}
	}

	return services, currentUnits, skippedNeverActive, skippedErrors
}

func (sm *systemdManager) removeStaleServices(currentUnits map[string]struct{}) {
	sm.Lock()
	defer sm.Unlock()

	for unitName := range sm.serviceStatsMap {
		if _, exists := currentUnits[unitName]; !exists {
			delete(sm.serviceStatsMap, unitName)
		}
	}
}

// updateServiceStats updates the statistics for a single systemd service.
func (sm *systemdManager) updateServiceStats(ctx context.Context, conn *dbus.Conn, unit dbus.UnitStatus) (*systemd.Service, error) {
	sm.Lock()
	defer sm.Unlock()

	// if service has never been active (no active since time), skip it
	if activeEnterTsProp, err := conn.GetUnitTypePropertyContext(ctx, unit.Name, "Unit", "ActiveEnterTimestamp"); err == nil {
		if ts, ok := activeEnterTsProp.Value.Value().(uint64); !ok || ts == 0 || ts == math.MaxUint64 {
			return nil, errNoActiveTime
		}
	} else {
		return nil, err
	}

	service, serviceExists := sm.serviceStatsMap[unit.Name]
	if !serviceExists {
		service = &systemd.Service{Name: unescapeServiceName(strings.TrimSuffix(unit.Name, ".service"))}
		sm.serviceStatsMap[unit.Name] = service
	}

	memPeak := service.MemPeak
	if memPeakProp, err := conn.GetUnitTypePropertyContext(ctx, unit.Name, "Service", "MemoryPeak"); err == nil {
		// If memPeak is MaxUint64 the api is saying it's not available
		if v, ok := memPeakProp.Value.Value().(uint64); ok && v != math.MaxUint64 {
			memPeak = v
		}
	}

	var memUsage uint64
	if memProp, err := conn.GetUnitTypePropertyContext(ctx, unit.Name, "Service", "MemoryCurrent"); err == nil {
		// If memUsage is MaxUint64 the api is saying it's not available
		if v, ok := memProp.Value.Value().(uint64); ok && v != math.MaxUint64 {
			memUsage = v
		}
	}

	service.State = systemd.ParseServiceStatus(unit.ActiveState)
	service.Sub = systemd.ParseServiceSubState(unit.SubState)

	// some systems always return 0 for mem peak, so we should update the peak if the current usage is greater
	if memUsage > memPeak {
		memPeak = memUsage
	}

	var cpuUsage uint64
	if cpuProp, err := conn.GetUnitTypePropertyContext(ctx, unit.Name, "Service", "CPUUsageNSec"); err == nil {
		if v, ok := cpuProp.Value.Value().(uint64); ok {
			cpuUsage = v
		}
	}

	service.Mem = memUsage
	if memPeak > service.MemPeak {
		service.MemPeak = memPeak
	}
	service.UpdateCPUPercent(cpuUsage)

	return service, nil
}

// unescapeServiceName unescapes systemd service names that contain C-style escape sequences like \x2d
func unescapeServiceName(name string) string {
	if !strings.Contains(name, "\\x") {
		return name
	}
	unescaped, err := strconv.Unquote("\"" + name + "\"")
	if err != nil {
		return name
	}
	return unescaped
}

// getServicePatterns returns the list of service patterns to match.
// It reads from the SERVICE_PATTERNS environment variable if set,
// otherwise defaults to "*service".
func getServicePatterns() []string {
	patterns := []string{}
	if envPatterns, _ := utils.GetEnv("SERVICE_PATTERNS"); envPatterns != "" {
		for pattern := range strings.SplitSeq(envPatterns, ",") {
			pattern = strings.TrimSpace(pattern)
			if pattern == "" {
				continue
			}
			if !strings.HasSuffix(pattern, "timer") && !strings.HasSuffix(pattern, ".service") {
				pattern += ".service"
			}
			patterns = append(patterns, pattern)
		}
	}
	if len(patterns) == 0 {
		patterns = []string{"*.service"}
	}
	return patterns
}
