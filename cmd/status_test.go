package cmd

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apimodel "github.com/mordilloSan/go-monitoring/internal/api/model"
	"github.com/mordilloSan/go-monitoring/internal/config"
)

func TestStatusTargetDisabled(t *testing.T) {
	_, err := statusTarget("none")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP API is disabled")
}

func TestStatusBaseURL(t *testing.T) {
	tests := []struct {
		addr     string
		expected string
	}{
		{addr: "127.0.0.1:45876", expected: "http://127.0.0.1:45876"},
		{addr: ":9000", expected: "http://127.0.0.1:9000"},
		{addr: "0.0.0.0:9000", expected: "http://127.0.0.1:9000"},
		{addr: "[::]:9000", expected: "http://127.0.0.1:9000"},
		{addr: "[::1]:9000", expected: "http://[::1]:9000"},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			baseURL, err := statusBaseURL(tt.addr)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, baseURL)
		})
	}
}

func TestPrintStatusOverUnixSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "agent.sock")
	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(healthResponse{Healthy: true, LastUpdated: "now"})
	})
	mux.HandleFunc("/api/v1/meta", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(apimodel.MetaResponse{
			Version: "test",
			Listeners: []apimodel.ListenerMeta{
				{Name: "metrics", EffectiveAddress: socketPath, APIs: []string{config.APIKindMetrics}, Active: true},
			},
		})
	})
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	cfg := config.Default()
	cfg.Listeners = []config.Listener{{Name: "metrics", Address: "unix:" + socketPath, APIs: []string{config.APIKindMetrics}}}

	out, err := captureStdout(t, func() error {
		return printStatus(context.Background(), cfg)
	})
	require.NoError(t, err)
	assert.Contains(t, out, "Agent: unix:"+socketPath)
	assert.Contains(t, out, "Version: test")
	assert.Contains(t, out, "Health: true")
}
