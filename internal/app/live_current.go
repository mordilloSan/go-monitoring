package app

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mordilloSan/go-monitoring/internal/domain/system"
	"github.com/mordilloSan/go-monitoring/internal/store"
	"github.com/mordilloSan/go-monitoring/internal/utils"
)

const liveSystemSummaryKey = "system_summary"

var baseSystemPluginNames = []string{
	store.PluginCPU,
	store.PluginMem,
	store.PluginSwap,
	store.PluginLoad,
	store.PluginDiskIO,
	store.PluginFS,
	store.PluginNetwork,
	store.PluginGPU,
	store.PluginSensors,
}

type liveCacheEntry struct {
	capturedAt int64
	raw        json.RawMessage
	expiresAt  time.Time
}

func liveCurrentTTLsFromEnv(env func(string) (string, bool)) map[string]time.Duration {
	ttls := map[string]time.Duration{
		liveSystemSummaryKey:    2 * time.Second,
		store.PluginCPU:         2 * time.Second,
		store.PluginMem:         2 * time.Second,
		store.PluginSwap:        2 * time.Second,
		store.PluginLoad:        2 * time.Second,
		store.PluginDiskIO:      2 * time.Second,
		store.PluginFS:          5 * time.Second,
		store.PluginNetwork:     2 * time.Second,
		store.PluginGPU:         2 * time.Second,
		store.PluginSensors:     2 * time.Second,
		store.PluginContainers:  5 * time.Second,
		store.PluginSystemd:     30 * time.Second,
		store.PluginProcesses:   10 * time.Second,
		store.PluginPrograms:    10 * time.Second,
		store.PluginConnections: 10 * time.Second,
		store.PluginIRQ:         2 * time.Second,
		store.PluginSmart:       30 * time.Second,
	}

	if raw, ok := env("API_CACHE_DEFAULT"); ok {
		if ttl, valid := parseAPICacheTTL("API_CACHE_DEFAULT", raw); valid {
			for key := range ttls {
				ttls[key] = ttl
			}
		}
	}
	if raw, ok := env("API_CACHE_EXPENSIVE"); ok {
		if ttl, valid := parseAPICacheTTL("API_CACHE_EXPENSIVE", raw); valid {
			for _, key := range []string{
				store.PluginContainers,
				store.PluginSystemd,
				store.PluginProcesses,
				store.PluginPrograms,
				store.PluginConnections,
				store.PluginSmart,
			} {
				ttls[key] = ttl
			}
		}
	}

	keys := append(store.PluginNames(), liveSystemSummaryKey)
	for _, key := range keys {
		envName := "API_CACHE_" + strings.ToUpper(key)
		if raw, ok := env(envName); ok {
			if ttl, valid := parseAPICacheTTL(envName, raw); valid {
				ttls[key] = ttl
			}
		}
	}
	return ttls
}

func parseAPICacheTTL(name, raw string) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	ttl, err := time.ParseDuration(raw)
	if err != nil || ttl < 0 {
		slog.Warn("Invalid API cache TTL", "name", name, "value", raw, "err", err)
		return 0, false
	}
	return ttl, true
}

func (a *App) CurrentPlugin(plugin string) (int64, json.RawMessage, error) {
	if !store.IsPluginName(plugin) {
		return 0, nil, fmt.Errorf("unknown plugin %q", plugin)
	}
	switch plugin {
	case store.PluginProcesses, store.PluginPrograms:
		return a.currentProcessPlugin(plugin)
	default:
		return a.currentCachedRaw(plugin, a.liveTTL(plugin), func() (int64, json.RawMessage, error) {
			return a.collectCurrentPlugin(plugin)
		})
	}
}

func (a *App) SystemSummary() (int64, system.Summary, error) {
	capturedAt, raw, err := a.currentCachedRaw(liveSystemSummaryKey, a.liveTTL(liveSystemSummaryKey), func() (int64, json.RawMessage, error) {
		cacheKey := liveCacheTimeKey(a.liveTTL(liveSystemSummaryKey))
		data := a.collectLiveCurrentData(cacheKey, true, false)
		capturedAt := time.Now().UTC().UnixMilli()
		if _, err := a.cachePluginPayloads(data, capturedAt, false); err != nil {
			return 0, nil, err
		}
		summary := system.NewSummary(data)
		raw, err := json.Marshal(summary)
		if err != nil {
			return 0, nil, err
		}
		return capturedAt, raw, nil
	})
	if err != nil {
		return 0, system.Summary{}, err
	}
	var summary system.Summary
	if err := json.Unmarshal(raw, &summary); err != nil {
		return 0, system.Summary{}, err
	}
	return capturedAt, summary, nil
}

func (a *App) collectCurrentPlugin(plugin string) (int64, json.RawMessage, error) {
	switch plugin {
	case store.PluginConnections:
		return marshalCurrentPlugin(collectConnectionStats())
	case store.PluginIRQ:
		return marshalCurrentPlugin(collectIRQStats())
	case store.PluginSystemd:
		if a.systemdManager == nil {
			return marshalCurrentPlugin([]any{})
		}
		return marshalCurrentPlugin(a.systemdManager.getServiceStats(a.systemdManager.context(), nil, true))
	case store.PluginSmart:
		return marshalCurrentPlugin(a.currentSmartRecords())
	}

	cacheKey := liveCacheTimeKey(a.liveTTL(plugin))
	return a.collectSystemPlugin(plugin, cacheKey, plugin == store.PluginContainers)
}

func (a *App) currentProcessPlugin(plugin string) (int64, json.RawMessage, error) {
	if capturedAt, raw, ok := a.getLiveCache(plugin); ok {
		return capturedAt, raw, nil
	}

	a.Lock()
	count, processes, programs := a.processManager.collectProcessStats()
	a.Unlock()

	capturedAt := time.Now().UTC().UnixMilli()
	processRaw, err := json.Marshal(store.ProcessesData{Count: count, Items: processes})
	if err != nil {
		return 0, nil, err
	}
	programsRaw, err := json.Marshal(programs)
	if err != nil {
		return 0, nil, err
	}
	a.setLiveCache(store.PluginProcesses, capturedAt, processRaw, a.liveTTL(store.PluginProcesses))
	a.setLiveCache(store.PluginPrograms, capturedAt, programsRaw, a.liveTTL(store.PluginPrograms))

	if plugin == store.PluginPrograms {
		return capturedAt, json.RawMessage(append([]byte(nil), programsRaw...)), nil
	}
	return capturedAt, json.RawMessage(append([]byte(nil), processRaw...)), nil
}

func (a *App) collectLiveCurrentData(cacheKey uint16, includeDetails, includeContainers bool) *system.CombinedData {
	a.Lock()
	defer a.Unlock()

	data := &system.CombinedData{
		Stats: a.getSystemStats(cacheKey),
		Info:  a.systemInfoManager.systemInfo,
	}
	if includeContainers && a.dockerManager != nil {
		if containerStats, err := a.dockerManager.GetStats(cacheKey); err == nil {
			data.Containers = containerStats
		} else {
			slog.Debug("Containers", "err", err)
		}
	}
	a.attachFilesystemStats(data)
	if includeDetails {
		details := a.systemInfoManager.systemDetails
		data.Details = &details
	}
	return data
}

func (a *App) collectSystemPlugin(plugin string, cacheKey uint16, includeContainers bool) (int64, json.RawMessage, error) {
	data := a.collectLiveCurrentData(cacheKey, false, includeContainers)
	capturedAt := time.Now().UTC().UnixMilli()
	payloads, err := a.cachePluginPayloads(data, capturedAt, includeContainers)
	if err != nil {
		return 0, nil, err
	}
	raw, ok := payloads[plugin]
	if !ok {
		return 0, nil, fmt.Errorf("unknown plugin %q", plugin)
	}
	return capturedAt, raw, nil
}

func (a *App) cachePluginPayloads(data *system.CombinedData, capturedAt int64, includeContainers bool) (map[string]json.RawMessage, error) {
	payloads := store.SnapshotPluginPayloads(data)
	keys := baseSystemPluginNames
	if includeContainers {
		keys = append(append([]string{}, baseSystemPluginNames...), store.PluginContainers)
	}

	rawPayloads := make(map[string]json.RawMessage, len(keys))
	for _, key := range keys {
		payload, ok := payloads[key]
		if !ok {
			continue
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		rawPayloads[key] = json.RawMessage(append([]byte(nil), raw...))
		a.setLiveCache(key, capturedAt, raw, a.liveTTL(key))
	}
	return rawPayloads, nil
}

func (a *App) attachFilesystemStats(data *system.CombinedData) {
	data.Stats.ExtraFs = make(map[string]*system.FsStats)
	data.Info.ExtraFsPct = make(map[string]float64)
	for name, stats := range a.fsManager.fsStats {
		if !stats.Root && stats.DiskTotal > 0 {
			key := name
			if stats.Name != "" {
				key = stats.Name
			}
			data.Stats.ExtraFs[key] = stats
			data.Info.ExtraFsPct[key] = utils.TwoDecimals((stats.DiskUsed / stats.DiskTotal) * 100)
		}
	}
}

func (a *App) currentSmartRecords() []store.SmartDeviceRecord {
	if a.smartManager == nil {
		return []store.SmartDeviceRecord{}
	}
	items := a.smartManager.GetCurrentData()
	records := make([]store.SmartDeviceRecord, 0, len(items))
	for key, item := range items {
		id := key
		if item.DiskName != "" {
			id = item.DiskName
		}
		records = append(records, store.SmartDeviceRecord{
			ID:   id,
			Key:  key,
			Data: item,
		})
	}
	return records
}

func marshalCurrentPlugin(payload any) (int64, json.RawMessage, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	return time.Now().UTC().UnixMilli(), raw, nil
}

func (a *App) currentCachedRaw(key string, ttl time.Duration, collect func() (int64, json.RawMessage, error)) (int64, json.RawMessage, error) {
	if capturedAt, raw, ok := a.getLiveCache(key); ok {
		return capturedAt, raw, nil
	}
	capturedAt, raw, err := collect()
	if err != nil {
		return 0, nil, err
	}
	a.setLiveCache(key, capturedAt, raw, ttl)
	return capturedAt, json.RawMessage(append([]byte(nil), raw...)), nil
}

func (a *App) getLiveCache(key string) (int64, json.RawMessage, bool) {
	a.liveMu.Lock()
	defer a.liveMu.Unlock()

	entry, ok := a.liveCache[key]
	if !ok || time.Now().After(entry.expiresAt) {
		return 0, nil, false
	}
	return entry.capturedAt, json.RawMessage(append([]byte(nil), entry.raw...)), true
}

func (a *App) setLiveCache(key string, capturedAt int64, raw json.RawMessage, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	a.liveMu.Lock()
	defer a.liveMu.Unlock()

	if a.liveCache == nil {
		a.liveCache = make(map[string]liveCacheEntry)
	}
	a.liveCache[key] = liveCacheEntry{
		capturedAt: capturedAt,
		raw:        json.RawMessage(append([]byte(nil), raw...)),
		expiresAt:  time.Now().Add(ttl),
	}
}

func (a *App) liveTTL(key string) time.Duration {
	if ttl, ok := a.liveTTLs[key]; ok {
		return ttl
	}
	return 2 * time.Second
}

func liveCacheTimeKey(ttl time.Duration) uint16 {
	ms := min(max(ttl.Milliseconds(), 1000), 65_000)
	if uint16(ms) == defaultDataCacheTimeMs {
		ms--
	}
	return uint16(ms)
}
