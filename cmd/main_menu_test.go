package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mordilloSan/go-monitoring/internal/config"
)

func TestParseMenuCommand(t *testing.T) {
	var opts cmdOptions
	handled, code := opts.parse([]string{"cmd", "menu"})

	assert.False(t, handled)
	assert.Equal(t, 0, code)
	assert.Equal(t, commandMenu, opts.command)
	assert.Equal(t, config.DefaultPath(), opts.configPath)
}

func TestRunMainMenuRequiresTerminal(t *testing.T) {
	// Test stdin is not a terminal, so the menu must refuse to start.
	code := runMainMenu(context.Background(), cmdOptions{command: commandMenu})
	assert.Equal(t, 1, code)
}

func TestMainMenuPauseReturnsOnCanceledContext(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	defer reader.Close()
	defer writer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out bytes.Buffer
	menu := &configMenu{in: reader, out: &out}

	err = menu.pause(ctx)

	require.ErrorIs(t, err, errConfigMenuInterrupted)
	assert.Contains(t, out.String(), "Press Enter to return to the menu.")
}

func TestPrintMenuStatusErrorConnectionRefused(t *testing.T) {
	var out bytes.Buffer

	printMenuStatusError(&out, fmt.Errorf("dial failed: %w", syscall.ECONNREFUSED))

	assert.Contains(t, out.String(), "Agent API is not reachable.")
	assert.Contains(t, out.String(), "systemctl start go-monitoring.service")
}

func TestBuildAgentItemsDisablesStatusWhenUnavailable(t *testing.T) {
	items := buildAgentItems(false, false)

	require.Len(t, items, 5)
	assert.Equal(t, "Start agent (background)", items[1].label)
	assert.False(t, items[1].disabled)
	assert.Equal(t, "Stop/remove background agent", items[2].label)
	assert.True(t, items[2].disabled)
	assert.Equal(t, "Agent status (not running)", items[3].label)
	assert.True(t, items[3].disabled)
}

func TestBuildAgentItemsDisablesStartWhenRunning(t *testing.T) {
	items := buildAgentItems(true, true)

	require.Len(t, items, 5)
	assert.Equal(t, "Start agent (background, already running)", items[1].label)
	assert.True(t, items[1].disabled)
	assert.False(t, items[2].disabled)
	assert.Equal(t, "Agent status", items[3].label)
	assert.False(t, items[3].disabled)
}

func TestMenuCursorSkipsDisabledItems(t *testing.T) {
	items := []menuItem{
		{label: "one"},
		{label: "two", disabled: true},
		{label: "three"},
	}

	cursor := 1
	assert.True(t, normalizeMenuCursor(items, &cursor))
	assert.Equal(t, 0, cursor)
	assert.Equal(t, 2, nextEnabledMenuIndex(items, cursor, 1))
	assert.Equal(t, 2, nextEnabledMenuIndex(items, cursor, -1))
}

func TestResetAPIConfigPreservesGeneralSettings(t *testing.T) {
	cfg := config.Default()
	cfg.Listeners = nil
	cfg.CollectorInterval = config.Duration(42 * time.Second)
	cfg.History = "none"

	resetAPIConfig(&cfg)

	defaults := config.Default()
	assert.Equal(t, defaults.Listeners, cfg.Listeners)
	assert.Equal(t, defaults.CacheTTL, cfg.CacheTTL)
	assert.Equal(t, config.Duration(42*time.Second), cfg.CollectorInterval)
	assert.Equal(t, "none", cfg.History)
}

func TestDetachedRunArgsPreservesMenuOverrides(t *testing.T) {
	opts := cmdOptions{
		configPath:           "/tmp/config.json",
		listeners:            listenerListFlag{values: []config.Listener{{Name: "metrics", Address: "127.0.0.1:9000", APIs: []string{config.APIKindMetrics}}}},
		listenersSet:         true,
		collectorInterval:    42 * time.Second,
		collectorIntervalSet: true,
		history:              "cpu,mem",
		historySet:           true,
		apiCacheDefault:      2 * time.Second,
		apiCacheDefaultSet:   true,
		apiCacheExpensive:    10 * time.Second,
		apiCacheExpensiveSet: true,
		cacheTTL: durationMapFlag{values: map[string]time.Duration{
			"mem": 5 * time.Second,
			"cpu": 3 * time.Second,
		}},
	}

	assert.Equal(t, []string{
		"run",
		"--config", "/tmp/config.json",
		"--listener", "name=metrics,address=127.0.0.1:9000,apis=metrics",
		"--collector-interval", "42s",
		"--history", "cpu,mem",
		"--api-cache-default", "2s",
		"--api-cache-expensive", "10s",
		"--api-cache", "cpu=3s",
		"--api-cache", "mem=5s",
	}, detachedRunArgs(opts))
}

func TestSystemdUnitUnavailable(t *testing.T) {
	assert.True(t, systemdUnitUnavailable("Failed to start go-monitoring.service: Unit go-monitoring.service not found."))
	assert.False(t, systemdUnitUnavailable("Failed to start go-monitoring.service: permission denied."))
}

func TestLocalAgentArgsMatch(t *testing.T) {
	exe, err := os.Executable()
	require.NoError(t, err)

	assert.True(t, localAgentArgsMatch([]string{exe, "run", "--config", "/etc/go-monitoring/config.json"}, exe, "/etc/go-monitoring/config.json"))
	assert.True(t, localAgentArgsMatch([]string{exe, "run", "--config=/etc/go-monitoring/config.json"}, exe, "/etc/go-monitoring/config.json"))
	assert.False(t, localAgentArgsMatch([]string{exe, "menu", "--config", "/etc/go-monitoring/config.json"}, exe, "/etc/go-monitoring/config.json"))
	assert.False(t, localAgentArgsMatch([]string{exe, "run", "--config", "/tmp/other.json"}, exe, "/etc/go-monitoring/config.json"))
}

func TestSplitProcCmdline(t *testing.T) {
	assert.Equal(t, []string{"go-monitoring", "run"}, splitProcCmdline([]byte("go-monitoring\x00run\x00")))
	assert.Nil(t, splitProcCmdline(nil))
}
