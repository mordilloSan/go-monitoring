package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	"github.com/mordilloSan/go-monitoring/internal/domain/system"
	"github.com/mordilloSan/go-monitoring/internal/store"
	"github.com/mordilloSan/go-monitoring/internal/utils"
)

const LiveSystemSummaryKey = "system_summary"

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
	ttls := DefaultLiveCurrentTTLs()
	ApplyLiveCurrentTTLEnv(ttls, env)
	return ttls
}

func DefaultLiveCurrentTTLs() map[string]time.Duration {
	return map[string]time.Duration{
		LiveSystemSummaryKey:    2 * time.Second,
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
}

func ApplyLiveCurrentTTLEnv(ttls map[string]time.Duration, env func(string) (string, bool)) {
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

	keys := append(store.PluginNames(), LiveSystemSummaryKey)
	for _, key := range keys {
		envName := "API_CACHE_" + strings.ToUpper(key)
		if raw, ok := env(envName); ok {
			if ttl, valid := parseAPICacheTTL(envName, raw); valid {
				ttls[key] = ttl
			}
		}
	}
}

func (a *App) SetLiveCurrentTTLs(ttls map[string]time.Duration) {
	a.runtimeMu.Lock()
	defer a.runtimeMu.Unlock()
	a.liveTTLs = make(map[string]time.Duration, len(ttls))
	maps.Copy(a.liveTTLs, ttls)
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

func (a *App) CurrentPlugin(ctx context.Context, plugin string) (int64, json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	if !store.IsPluginName(plugin) {
		return 0, nil, fmt.Errorf("unknown plugin %q", plugin)
	}
	switch plugin {
	case store.PluginProcesses, store.PluginPrograms:
		return a.currentProcessPlugin(ctx, plugin)
	default:
		return a.currentCachedRaw(ctx, plugin, a.liveTTL(plugin), func() (int64, json.RawMessage, error) {
			return a.collectCurrentPlugin(ctx, plugin)
		})
	}
}

func (a *App) SystemSummary(ctx context.Context) (int64, system.Summary, error) {
	capturedAt, raw, err := a.currentCachedRaw(ctx, LiveSystemSummaryKey, a.liveTTL(LiveSystemSummaryKey), func() (int64, json.RawMessage, error) {
		cacheKey := liveCacheTimeKey(a.liveTTL(LiveSystemSummaryKey))
		data, err := a.collectLiveCurrentData(ctx, cacheKey, true, false)
		if err != nil {
			return 0, nil, err
		}
		capturedAt := time.Now().UTC().UnixMilli()
		if _, cacheErr := a.cachePluginPayloads(data, capturedAt, false); cacheErr != nil {
			return 0, nil, cacheErr
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

func (a *App) collectCurrentPlugin(ctx context.Context, plugin string) (int64, json.RawMessage, error) {
	switch plugin {
	case store.PluginConnections:
		stats, err := collectConnectionStats(ctx)
		if err != nil {
			return 0, nil, err
		}
		return marshalCurrentPlugin(stats)
	case store.PluginIRQ:
		stats, err := collectIRQStats(ctx)
		if err != nil {
			return 0, nil, err
		}
		return marshalCurrentPlugin(stats)
	case store.PluginSystemd:
		if a.systemdManager == nil {
			return marshalCurrentPlugin([]any{})
		}
		services, err := a.systemdManager.getServiceStats(ctx, nil, true)
		if err != nil {
			return 0, nil, err
		}
		return marshalCurrentPlugin(services)
	case store.PluginSmart:
		if err := ctx.Err(); err != nil {
			return 0, nil, err
		}
		return marshalCurrentPlugin(a.currentSmartRecords())
	}

	cacheKey := liveCacheTimeKey(a.liveTTL(plugin))
	return a.collectSystemPlugin(ctx, plugin, cacheKey, plugin == store.PluginContainers)
}

func (a *App) currentProcessPlugin(ctx context.Context, plugin string) (int64, json.RawMessage, error) {
	if capturedAt, raw, ok := a.getLiveCache(plugin); ok {
		return capturedAt, raw, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}

	a.Lock()
	count, processes, programs, err := a.processManager.collectProcessStats(ctx)
	a.Unlock()
	if err != nil {
		return 0, nil, err
	}

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

func (a *App) collectLiveCurrentData(ctx context.Context, cacheKey uint16, includeDetails, includeContainers bool) (*system.CombinedData, error) {
	a.Lock()
	defer a.Unlock()

	stats, err := a.getSystemStats(ctx, cacheKey)
	if err != nil {
		return nil, err
	}
	data := &system.CombinedData{
		Stats: stats,
		Info:  a.systemInfoManager.systemInfo,
	}
	if includeContainers && a.dockerManager != nil {
		if containerStats, err := a.dockerManager.GetStats(ctx, cacheKey); err == nil {
			data.Containers = containerStats
		} else if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		} else {
			slog.Debug("Containers", "err", err)
		}
	}
	a.attachFilesystemStats(data)
	if includeDetails {
		details := a.systemInfoManager.systemDetails
		data.Details = &details
	}
	return data, nil
}

func (a *App) collectSystemPlugin(ctx context.Context, plugin string, cacheKey uint16, includeContainers bool) (int64, json.RawMessage, error) {
	data, err := a.collectLiveCurrentData(ctx, cacheKey, false, includeContainers)
	if err != nil {
		return 0, nil, err
	}
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

func (a *App) currentCachedRaw(ctx context.Context, key string, ttl time.Duration, collect func() (int64, json.RawMessage, error)) (int64, json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
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
	a.runtimeMu.RLock()
	defer a.runtimeMu.RUnlock()
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
