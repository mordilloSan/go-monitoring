package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mordilloSan/go-monitoring/internal/app"
	"github.com/mordilloSan/go-monitoring/internal/config"
	"github.com/mordilloSan/go-monitoring/internal/store"
)

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	original := os.Stdout
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = writer
	defer func() {
		os.Stdout = original
	}()

	runErr := fn()
	require.NoError(t, writer.Close())

	var buf bytes.Buffer
	_, copyErr := io.Copy(&buf, reader)
	require.NoError(t, copyErr)
	require.NoError(t, reader.Close())
	return buf.String(), runErr
}

func createValidMetricsDB(t *testing.T, dataDir string) {
	t.Helper()

	s, err := store.OpenStore(dataDir)
	require.NoError(t, err)
	require.NoError(t, s.Close())
}

func TestGetAddress(t *testing.T) {
	tests := []struct {
		name     string
		listen   string
		envVars  map[string]string
		expected string
	}{
		{name: "default port", expected: "127.0.0.1:45876"},
		{name: "port only", listen: "8080", expected: "127.0.0.1:8080"},
		{name: "explicit address", listen: "127.0.0.1:9000", expected: "127.0.0.1:9000"},
		{name: "explicit all interfaces", listen: ":9000", expected: ":9000"},
		{
			name: "listen env",
			envVars: map[string]string{
				"LISTEN": "0.0.0.0:9001",
			},
			expected: "0.0.0.0:9001",
		},
		{
			name: "legacy port env",
			envVars: map[string]string{
				"PORT": "7000",
			},
			expected: "127.0.0.1:7000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for key, value := range tt.envVars {
				t.Setenv(key, value)
			}
			assert.Equal(t, tt.expected, app.GetAddress(tt.listen))
		})
	}
}

func TestParseFlags(t *testing.T) {
	defaultConfigPath := config.DefaultPath()

	tests := []struct {
		name         string
		args         []string
		expected     cmdOptions
		handled      bool
		expectedArgs []string
	}{
		{
			name:    "no args shows help",
			args:    []string{"cmd"},
			handled: true,
		},
		{
			name:     "run command",
			args:     []string{"cmd", "run"},
			expected: cmdOptions{command: commandRun, configPath: defaultConfigPath},
		},
		{
			name:     "listen flag",
			args:     []string{"cmd", "run", "--listen", "8080"},
			expected: cmdOptions{command: commandRun, configPath: defaultConfigPath, listen: "8080", listenSet: true},
		},
		{
			name:     "history flag",
			args:     []string{"cmd", "run", "--history", "cpu,mem"},
			expected: cmdOptions{command: commandRun, configPath: defaultConfigPath, history: "cpu,mem", historySet: true},
		},
		{
			name:     "collector interval flag",
			args:     []string{"cmd", "run", "--collector-interval", "30s"},
			expected: cmdOptions{command: commandRun, configPath: defaultConfigPath, collectorInterval: 30 * time.Second, collectorIntervalSet: true},
		},
		{
			name:         "legacy single dash listen",
			args:         []string{"cmd", "run", "-listen=8080"},
			expected:     cmdOptions{command: commandRun, configPath: defaultConfigPath, listen: "8080", listenSet: true},
			expectedArgs: []string{"cmd", "run", "-listen=8080"},
		},
		{
			name:     "dash run alias",
			args:     []string{"cmd", "-run", "--listen", "8080"},
			expected: cmdOptions{command: commandRun, configPath: defaultConfigPath, listen: "8080", listenSet: true},
		},
		{
			name:     "capital dash config alias",
			args:     []string{"cmd", "-Config", "--print"},
			expected: cmdOptions{command: commandConfig, configPath: defaultConfigPath, configPrint: true},
		},
		{
			name:     "db command defaults to check action",
			args:     []string{"cmd", "db"},
			expected: cmdOptions{command: commandDB, configPath: defaultConfigPath},
		},
		{
			name: "db reset command",
			args: []string{"cmd", "db", "reset", "--force", "--data-dir", "/tmp/go-monitoring"},
			expected: cmdOptions{
				command:    commandDB,
				configPath: defaultConfigPath,
				dataDir:    "/tmp/go-monitoring",
				dbForce:    true,
				dbAction:   "reset",
			},
		},
		{
			name:    "help command handled",
			args:    []string{"cmd", "help"},
			handled: true,
		},
		{
			name:    "version flag handled",
			args:    []string{"cmd", "--version"},
			handled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts cmdOptions
			handled, code := opts.parse(tt.args)

			assert.Equal(t, tt.handled, handled)
			assert.Equal(t, 0, code)
			if !handled {
				tt.expected.cacheTTL = opts.cacheTTL
				assert.Equal(t, tt.expected, opts)
			}
			if tt.expectedArgs != nil {
				assert.Equal(t, tt.expectedArgs, tt.args)
			}
		})
	}
}

func TestHandleDBCommandActions(t *testing.T) {
	t.Run("path", func(t *testing.T) {
		tmpDir := t.TempDir()

		out, err := captureStdout(t, func() error {
			return handleDBCommand(cmdOptions{command: commandDB, dataDir: tmpDir, dbAction: "path"})
		})

		require.NoError(t, err)
		assert.Equal(t, store.DatabasePath(tmpDir)+"\n", out)
	})

	t.Run("check", func(t *testing.T) {
		tmpDir := t.TempDir()
		createValidMetricsDB(t, tmpDir)

		_, err := captureStdout(t, func() error {
			return handleDBCommand(cmdOptions{command: commandDB, dataDir: tmpDir, dbAction: "check"})
		})

		require.NoError(t, err)
	})

	t.Run("maintain", func(t *testing.T) {
		tmpDir := t.TempDir()
		createValidMetricsDB(t, tmpDir)

		_, err := captureStdout(t, func() error {
			return handleDBCommand(cmdOptions{command: commandDB, dataDir: tmpDir, dbAction: "maintain"})
		})

		require.NoError(t, err)
	})

	t.Run("repair", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := store.DatabasePath(tmpDir)
		require.NoError(t, os.WriteFile(dbPath, []byte("not sqlite"), 0600))

		_, err := captureStdout(t, func() error {
			return handleDBCommand(cmdOptions{command: commandDB, dataDir: tmpDir, dbAction: "repair"})
		})

		require.NoError(t, err)
		assert.FileExists(t, dbPath)
		moved, err := filepath.Glob(dbPath + ".repair-*")
		require.NoError(t, err)
		assert.Len(t, moved, 1)
	})

	t.Run("reset with force", func(t *testing.T) {
		tmpDir := t.TempDir()
		createValidMetricsDB(t, tmpDir)
		dbPath := store.DatabasePath(tmpDir)

		_, err := captureStdout(t, func() error {
			return handleDBCommand(cmdOptions{command: commandDB, dataDir: tmpDir, dbAction: "reset", dbForce: true})
		})

		require.NoError(t, err)
		assert.FileExists(t, dbPath)
		moved, err := filepath.Glob(dbPath + ".reset-*")
		require.NoError(t, err)
		assert.Len(t, moved, 1)
	})

	t.Run("reset requires force", func(t *testing.T) {
		err := handleDBCommand(cmdOptions{command: commandDB, dataDir: t.TempDir(), dbAction: "reset"})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "db reset requires --force")
	})

	t.Run("unknown action", func(t *testing.T) {
		err := handleDBCommand(cmdOptions{command: commandDB, dataDir: t.TempDir(), dbAction: "nope"})

		require.Error(t, err)
		assert.Contains(t, err.Error(), `unknown db action "nope"`)
	})
}
