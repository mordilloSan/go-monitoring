package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mordilloSan/go-monitoring/internal/domain/system"
	"github.com/mordilloSan/go-monitoring/internal/store"
)

func newLiveCurrentTestApp() *App {
	return &App{
		cache:          NewSystemDataCache(),
		fsManager:      newFsManager(),
		networkManager: newNetworkManager(),
		processManager: newProcessManager(),
		sensorConfig: &SensorConfig{
			skipCollection: true,
			readSem:        make(chan struct{}, 1),
		},
		systemInfoManager: &systemInfoManager{
			systemInfo: system.Info{
				AgentVersion: "test-agent",
				Threads:      2,
			},
			systemDetails: system.Details{
				Hostname:    "live-host",
				OsName:      "Test OS",
				Arch:        "x86_64",
				CpuModel:    "test-cpu",
				Cores:       1,
				Threads:     2,
				MemoryTotal: 16 * 1024 * 1024 * 1024,
			},
		},
		liveCache: make(map[string]liveCacheEntry),
		liveTTLs:  DefaultLiveCurrentTTLs(),
	}
}

func TestLiveCurrentTTLsFromEnv(t *testing.T) {
	env := map[string]string{
		"API_CACHE_DEFAULT":        "1s",
		"API_CACHE_EXPENSIVE":      "12s",
		"API_CACHE_PROCESSES":      "3s",
		"API_CACHE_SYSTEM_SUMMARY": "1500ms",
	}
	ttls := liveCurrentTTLsFromEnv(func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	})

	assert.Equal(t, time.Second, ttls[store.PluginCPU])
	assert.Equal(t, 12*time.Second, ttls[store.PluginContainers])
	assert.Equal(t, 3*time.Second, ttls[store.PluginProcesses])
	assert.Equal(t, 12*time.Second, ttls[store.PluginPrograms])
	assert.Equal(t, 1500*time.Millisecond, ttls[LiveSystemSummaryKey])
}

func TestLiveCacheTimeKeyAvoidsCollectorKey(t *testing.T) {
	assert.Equal(t, defaultDataCacheTimeMs-1, liveCacheTimeKey(time.Minute))
	assert.Equal(t, uint16(1000), liveCacheTimeKey(0))
	assert.Equal(t, uint16(65_000), liveCacheTimeKey(2*time.Minute))
}

func TestCurrentPluginCanceledContextWinsOverCache(t *testing.T) {
	agent := &App{
		liveCache: map[string]liveCacheEntry{
			store.PluginCPU: {
				capturedAt: 123,
				raw:        json.RawMessage(`{"cpu":1}`),
				expiresAt:  time.Now().Add(time.Hour),
			},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := agent.CurrentPlugin(ctx, store.PluginCPU)

	assert.ErrorIs(t, err, context.Canceled)
}

func TestCurrentPluginGathersEveryPlugin(t *testing.T) {
	agent := newLiveCurrentTestApp()

	for _, plugin := range store.PluginNames() {
		t.Run(plugin, func(t *testing.T) {
			capturedAt, raw, err := agent.CurrentPlugin(context.Background(), plugin)

			require.NoError(t, err)
			assert.NotZero(t, capturedAt)
			assert.NotEmpty(t, raw)
			assert.True(t, json.Valid(raw), "raw plugin payload should be valid JSON: %s", raw)
		})
	}
}

func TestCurrentPluginPrimesSiblingSystemPluginCache(t *testing.T) {
	agent := newLiveCurrentTestApp()

	_, raw, err := agent.CurrentPlugin(context.Background(), store.PluginCPU)
	require.NoError(t, err)
	require.True(t, json.Valid(raw))

	for _, plugin := range baseSystemPluginNames {
		t.Run(plugin, func(t *testing.T) {
			_, cachedRaw, ok := agent.getLiveCache(plugin)

			require.True(t, ok, "expected %s to be cached by system plugin gather", plugin)
			assert.True(t, json.Valid(cachedRaw), "cached plugin payload should be valid JSON: %s", cachedRaw)
		})
	}

	_, _, ok := agent.getLiveCache(store.PluginContainers)
	assert.False(t, ok, "container payload should only be primed by container gathers")
}

func TestCurrentPluginSpecialPaths(t *testing.T) {
	agent := newLiveCurrentTestApp()

	_, systemdRaw, err := agent.CurrentPlugin(context.Background(), store.PluginSystemd)
	require.NoError(t, err)
	assert.JSONEq(t, `[]`, string(systemdRaw))

	_, smartRaw, err := agent.CurrentPlugin(context.Background(), store.PluginSmart)
	require.NoError(t, err)
	assert.JSONEq(t, `[]`, string(smartRaw))

	_, _, err = agent.CurrentPlugin(context.Background(), "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown plugin "nope"`)
}

func TestProcessAndProgramCurrentPluginsShareCollection(t *testing.T) {
	agent := newLiveCurrentTestApp()

	processCapturedAt, processRaw, err := agent.CurrentPlugin(context.Background(), store.PluginProcesses)
	require.NoError(t, err)
	require.True(t, json.Valid(processRaw))

	cachedProgramCapturedAt, cachedProgramRaw, ok := agent.getLiveCache(store.PluginPrograms)
	require.True(t, ok, "process collection should also cache program payload")
	require.True(t, json.Valid(cachedProgramRaw))

	programCapturedAt, programRaw, err := agent.CurrentPlugin(context.Background(), store.PluginPrograms)
	require.NoError(t, err)
	assert.Equal(t, processCapturedAt, cachedProgramCapturedAt)
	assert.Equal(t, cachedProgramCapturedAt, programCapturedAt)
	assert.JSONEq(t, string(cachedProgramRaw), string(programRaw))

	_, _, ok = agent.getLiveCache(store.PluginProcesses)
	assert.True(t, ok, "process payload should be cached after collection")
}

func TestSystemSummaryGathersAndPrimesSystemPluginCache(t *testing.T) {
	agent := newLiveCurrentTestApp()

	capturedAt, summary, err := agent.SystemSummary(context.Background())

	require.NoError(t, err)
	assert.NotZero(t, capturedAt)
	assert.Equal(t, "live-host", summary.Hostname)
	assert.Equal(t, "test-agent", summary.AgentVersion)
	assert.Equal(t, uint64(16*1024*1024*1024), summary.MemoryBytes)

	for _, plugin := range baseSystemPluginNames {
		t.Run(plugin, func(t *testing.T) {
			_, raw, ok := agent.getLiveCache(plugin)

			require.True(t, ok, "expected %s to be cached by system summary gather", plugin)
			assert.True(t, json.Valid(raw), "cached plugin payload should be valid JSON: %s", raw)
		})
	}
}
