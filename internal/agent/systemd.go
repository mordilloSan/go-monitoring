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

	conn, err := dbus.NewSystemConnectionContext(context.Background())
	if err != nil {
		slog.Debug("Error connecting to systemd", "err", err)
		return nil, err
	}
	// Priming connection: used once below for the initial getServiceStats call,
	// then released. Subsequent refreshes open and close their own connection.
	defer conn.Close()

	manager := &systemdManager{
		serviceStatsMap: make(map[string]*systemd.Service),
		patterns:        getServicePatterns(),
	}

	manager.startWorker(conn)

	return manager, nil
}

func (sm *systemdManager) startWorker(conn *dbus.Conn) {
	if sm.isRunning {
		return
	}
	sm.isRunning = true
	// prime the service stats map with the current services
	_ = sm.getServiceStats(conn, true)
	// update the services every 10 minutes
	go func() {
		for {
			time.Sleep(time.Minute * 10)
			_ = sm.getServiceStats(nil, true)
		}
	}()
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
func (sm *systemdManager) getServiceStats(conn *dbus.Conn, refresh bool) []*systemd.Service {
	// start := time.Now()
	// defer func() {
	// 	slog.Info("systemdManager.getServiceStats", "duration", time.Since(start))
	// }()

	var services []*systemd.Service
	var err error

	if !refresh {
		// return nil
		sm.Lock()
		defer sm.Unlock()
		for _, service := range sm.serviceStatsMap {
			services = append(services, service)
		}
		sm.hasFreshStats = false
		return services
	}

	if conn == nil || !conn.Connected() {
		conn, err = dbus.NewSystemConnectionContext(context.Background())
		if err != nil {
			return nil
		}
		defer conn.Close()
	}

	units, err := conn.ListUnitsByPatternsContext(context.Background(), []string{"loaded"}, sm.patterns)
	if err != nil {
		slog.Error("Error listing systemd service units", "err", err)
		return nil
	}

	// Track which units are currently present to remove stale entries
	currentUnits := make(map[string]struct{}, len(units))

	for _, unit := range units {
		currentUnits[unit.Name] = struct{}{}
		service, err := sm.updateServiceStats(conn, unit)
		if err != nil {
			continue
		}
		services = append(services, service)
	}

	// Remove services that no longer exist in systemd
	sm.Lock()
	for unitName := range sm.serviceStatsMap {
		if _, exists := currentUnits[unitName]; !exists {
			delete(sm.serviceStatsMap, unitName)
		}
	}
	sm.Unlock()

	sm.hasFreshStats = true
	return services
}

// updateServiceStats updates the statistics for a single systemd service.
func (sm *systemdManager) updateServiceStats(conn *dbus.Conn, unit dbus.UnitStatus) (*systemd.Service, error) {
	sm.Lock()
	defer sm.Unlock()

	ctx := context.Background()

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
