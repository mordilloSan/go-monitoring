package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mordilloSan/go-monitoring/internal/app"
	"github.com/mordilloSan/go-monitoring/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadMissingReturnsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")

	cfg, loaded, err := Load(path)

	require.NoError(t, err)
	assert.False(t, loaded)
	assert.Equal(t, ":45876", cfg.Listen)
	assert.Equal(t, app.DefaultCollectorInterval, cfg.CollectorInterval.Duration())
	assert.Equal(t, 5*time.Second, cfg.CacheTTL[store.PluginContainers].Duration())
}

func TestLoadMergesPartialConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{
  "collector_interval": "30s",
  "cache_ttl": {
    "containers": "9s"
  }
}`), 0o644))

	cfg, loaded, err := Load(path)

	require.NoError(t, err)
	assert.True(t, loaded)
	assert.Equal(t, 30*time.Second, cfg.CollectorInterval.Duration())
	assert.Equal(t, 9*time.Second, cfg.CacheTTL[store.PluginContainers].Duration())
	assert.Equal(t, 2*time.Second, cfg.CacheTTL[store.PluginCPU].Duration())
}

func TestApplyEnvKeepsLegacyOverrides(t *testing.T) {
	cfg := Default()
	ApplyEnv(&cfg, func(key string) (string, bool) {
		values := map[string]string{
			"PORT":                 "9000",
			"HISTORY":              "cpu",
			"API_CACHE_DEFAULT":    "1s",
			"API_CACHE_CONTAINERS": "8s",
		}
		value, ok := values[key]
		return value, ok
	})

	assert.Equal(t, "9000", cfg.Listen)
	assert.Equal(t, "cpu", cfg.History)
	assert.Equal(t, time.Second, cfg.CacheTTL[store.PluginCPU].Duration())
	assert.Equal(t, 8*time.Second, cfg.CacheTTL[store.PluginContainers].Duration())
}

func TestSaveIfMissingCreatesOnlyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := Default()
	cfg.CollectorInterval = Duration(15 * time.Second)

	created, err := SaveIfMissing(path, cfg)
	require.NoError(t, err)
	assert.True(t, created)

	cfg.CollectorInterval = Duration(30 * time.Second)
	created, err = SaveIfMissing(path, cfg)
	require.NoError(t, err)
	assert.False(t, created)

	loaded, exists, err := Load(path)
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, 15*time.Second, loaded.CollectorInterval.Duration())
}
