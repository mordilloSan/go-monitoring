package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/mordilloSan/go-monitoring/internal/store"
)

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
