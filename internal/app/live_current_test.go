package app

import (
	"testing"
	"time"

	"github.com/mordilloSan/go-monitoring/internal/store"
	"github.com/stretchr/testify/assert"
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
	assert.Equal(t, 1500*time.Millisecond, ttls[liveSystemSummaryKey])
}

func TestLiveCacheTimeKeyAvoidsCollectorKey(t *testing.T) {
	assert.Equal(t, uint16(defaultDataCacheTimeMs-1), liveCacheTimeKey(time.Minute))
	assert.Equal(t, uint16(1000), liveCacheTimeKey(0))
	assert.Equal(t, uint16(65_000), liveCacheTimeKey(2*time.Minute))
}
