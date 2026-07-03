package cmd

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mordilloSan/go-monitoring/internal/config"
)

func TestConfigMenuReadKeyInterrupt(t *testing.T) {
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	defer reader.Close()

	_, err = writer.Write([]byte{0x03})
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	menu := &configMenu{in: reader}
	key, err := menu.readKey()

	require.NoError(t, err)
	assert.Equal(t, menuKeyInterrupt, key)
}

func TestConfigMenuExitLabel(t *testing.T) {
	defaultItems := mainMenuItems(config.Default(), defaultConfigMenuOptions().exitLabel)
	sectionItems := mainMenuItems(config.Default(), "Back")

	assert.Equal(t, "Exit without saving", defaultItems[len(defaultItems)-1])
	assert.Equal(t, "Back", sectionItems[len(sectionItems)-1])
}

func TestListenMenuValidation(t *testing.T) {
	require.NoError(t, validateTCPListen("9000"))
	require.NoError(t, validateTCPListen("127.0.0.1:9000"))
	require.NoError(t, validateTCPListen(":9000"))

	require.Error(t, validateTCPListen("none"))
	require.Error(t, validateTCPListen("unix:/run/go-monitoring/agent.sock"))

	require.NoError(t, validateUnixSocketPath("/run/go-monitoring/agent.sock"))
	require.NoError(t, validateUnixSocketPath("unix:/run/go-monitoring/agent.sock"))

	require.Error(t, validateUnixSocketPath("relative.sock"))
	require.Error(t, validateUnixSocketPath("127.0.0.1:9000"))
}

func TestListenMenuDefaults(t *testing.T) {
	assert.Equal(t, "127.0.0.1:9000", tcpListenPromptDefault("9000"))
	assert.Equal(t, defaultTCPListen, tcpListenPromptDefault("none"))
	assert.Equal(t, defaultTCPListen, tcpListenPromptDefault("unix:/run/go-monitoring/agent.sock"))

	assert.Equal(t, "/tmp/agent.sock", unixSocketPromptDefault("unix:/tmp/agent.sock"))
	assert.Equal(t, defaultUnixSocketPath, unixSocketPromptDefault("none"))

	assert.Equal(t, "unix:/tmp/agent.sock", unixSocketListenValue("/tmp/agent.sock"))
	assert.Equal(t, "unix:/tmp/agent.sock", unixSocketListenValue("unix:/tmp/agent.sock"))
}

func TestAPIListenSummary(t *testing.T) {
	assert.Equal(t, "unix: no; HTTP: yes (:45876)", apiListenSummary(":45876"))
	assert.Equal(t, "unix: yes (/run/go-monitoring/agent.sock); HTTP: no", apiListenSummary("unix:/run/go-monitoring/agent.sock"))
	assert.Equal(t, "unix: no; HTTP: no", apiListenSummary("none"))
}

func TestBuildListenItemsShowsHTTPAndUnixState(t *testing.T) {
	tcpItems := buildListenItems(":45876")
	assert.Equal(t, "HTTP listener: yes", tcpItems[0])
	assert.Equal(t, "Unix socket: no", tcpItems[4])

	unixItems := buildListenItems("unix:/run/go-monitoring/agent.sock")
	assert.Equal(t, "HTTP listener: no", unixItems[0])
	assert.Equal(t, "Unix socket: yes", unixItems[4])
}

func TestHandleListenEnterTogglesSingleListenerMode(t *testing.T) {
	menu := &configMenu{}

	cfg := config.Default()
	cfg.Listen = ":45876"
	done, err := menu.handleListenEnter(0, &cfg, len(buildListenItems(cfg.Listen)))
	require.NoError(t, err)
	assert.False(t, done)
	assert.Equal(t, "none", cfg.Listen)

	done, err = menu.handleListenEnter(4, &cfg, len(buildListenItems(cfg.Listen)))
	require.NoError(t, err)
	assert.False(t, done)
	assert.Equal(t, defaultUnixSocketListen, cfg.Listen)

	done, err = menu.handleListenEnter(0, &cfg, len(buildListenItems(cfg.Listen)))
	require.NoError(t, err)
	assert.False(t, done)
	assert.Equal(t, defaultTCPListen, cfg.Listen)
}

func TestResetGeneralConfigPreservesAPISettings(t *testing.T) {
	cfg := config.Default()
	cfg.Listen = "unix:/tmp/custom.sock"
	cfg.CollectorInterval = config.Duration(42 * time.Second)
	cfg.History = "none"

	resetGeneralConfig(&cfg)

	defaults := config.Default()
	assert.Equal(t, defaults.CollectorInterval, cfg.CollectorInterval)
	assert.Equal(t, defaults.History, cfg.History)
	assert.Equal(t, "unix:/tmp/custom.sock", cfg.Listen)
}
