package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mordilloSan/go-monitoring/internal/app"
	"github.com/mordilloSan/go-monitoring/internal/store"
)

func TestLoadMissingReturnsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")

	cfg, loaded, err := Load(path)

	require.NoError(t, err)
	assert.False(t, loaded)
	assert.Equal(t, CurrentVersion, cfg.Version)
	metrics, ok := MetricsListener(cfg)
	require.True(t, ok)
	assert.Equal(t, "127.0.0.1:45876", metrics.Address)
	commands, ok := CommandListener(cfg)
	require.True(t, ok)
	assert.Contains(t, commands.Address, "agent.sock")
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
	assert.NotEmpty(t, cfg.Listeners)
	assert.Equal(t, 9*time.Second, cfg.CacheTTL[store.PluginContainers].Duration())
	assert.Equal(t, 2*time.Second, cfg.CacheTTL[store.PluginCPU].Duration())
}

func TestLoadExplicitEmptyListenersDisablesAPI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"listeners":[]}`), 0o644))

	cfg, loaded, err := Load(path)

	require.NoError(t, err)
	assert.True(t, loaded)
	assert.Empty(t, cfg.Listeners)
}

func TestApplyEnvKeepsSupportedOverrides(t *testing.T) {
	cfg := Default()
	ApplyEnv(&cfg, func(key string) (string, bool) {
		values := map[string]string{
			"HISTORY":              "cpu",
			"API_CACHE_DEFAULT":    "1s",
			"API_CACHE_CONTAINERS": "8s",
		}
		value, ok := values[key]
		return value, ok
	})

	metrics, ok := MetricsListener(cfg)
	require.True(t, ok)
	assert.Equal(t, "127.0.0.1:45876", metrics.Address)
	assert.Equal(t, "cpu", cfg.History)
	assert.Equal(t, time.Second, cfg.CacheTTL[store.PluginCPU].Duration())
	assert.Equal(t, 8*time.Second, cfg.CacheTTL[store.PluginContainers].Duration())
}

func TestValidateRejectsRemoteCommandListenerWithoutOverride(t *testing.T) {
	cfg := Default()
	cfg.Listeners = []Listener{{Name: "remote", Address: ":45876", APIs: []string{APIKindCommands}}}

	err := Validate(cfg)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "allow_remote_commands")

	cfg.AllowRemoteCommands = true
	require.NoError(t, Validate(cfg))
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

func TestValidateRejectsUnsupportedVersion(t *testing.T) {
	cfg := Default()
	cfg.Version = CurrentVersion + 1

	err := Validate(cfg)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported config version")
}
