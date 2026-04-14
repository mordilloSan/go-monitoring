package agent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/entities/smart"
	healthpkg "github.com/henrygd/beszel/pkg/agent/health"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newHTTPTestAgent(t *testing.T) *Agent {
	t.Helper()

	tmpDir := t.TempDir()
	store, err := OpenStore(tmpDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	capturedAt := time.Now().UTC().UnixMilli()
	require.NoError(t, store.WriteSnapshot(capturedAt, sampleCombinedData(55)))
	require.NoError(t, store.WriteSmartDevices(capturedAt, map[string]smart.SmartData{
		"/dev/sdb": {
			ModelName:   "disk-b",
			DiskName:    "/dev/sdb",
			SmartStatus: "passed",
		},
	}))
	require.NoError(t, healthpkg.Update())
	t.Cleanup(func() { _ = healthpkg.CleanUp() })

	return &Agent{
		dataDir:              tmpDir,
		store:                store,
		listenAddr:           ":45876",
		smartRefreshInterval: time.Hour,
	}
}

func TestHTTPRoutes(t *testing.T) {
	a := newHTTPTestAgent(t)
	handler := a.routes(time.Minute)

	tests := []struct {
		name   string
		method string
		path   string
		status int
		body   string
	}{
		{name: "health", method: http.MethodGet, path: "/healthz", status: http.StatusOK, body: `"healthy":true`},
		{name: "meta", method: http.MethodGet, path: "/api/v1/meta", status: http.StatusOK, body: `"collector_interval":"1m0s"`},
		{name: "summary", method: http.MethodGet, path: "/api/v1/summary", status: http.StatusOK, body: `"cpu":55`},
		{name: "system history", method: http.MethodGet, path: "/api/v1/history/system?resolution=1m&from=0&to=9999999999999&limit=10", status: http.StatusOK, body: `"resolution":"1m"`},
		{name: "container history", method: http.MethodGet, path: "/api/v1/history/containers?resolution=1m&from=0&to=9999999999999&limit=10", status: http.StatusOK, body: `"resolution":"1m"`},
		{name: "containers", method: http.MethodGet, path: "/api/v1/containers", status: http.StatusOK, body: `"image":"nginx"`},
		{name: "systemd", method: http.MethodGet, path: "/api/v1/systemd", status: http.StatusOK, body: `"n":"nginx.service"`},
		{name: "smart", method: http.MethodGet, path: "/api/v1/smart", status: http.StatusOK, body: `"dn":"/dev/sdb"`},
		{name: "invalid history", method: http.MethodGet, path: "/api/v1/history/system?resolution=bad", status: http.StatusBadRequest, body: `"error":"invalid resolution"`},
		{name: "smart refresh", method: http.MethodPost, path: "/api/v1/smart/refresh", status: http.StatusOK, body: `"items":[]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.status, rec.Code)
			assert.Contains(t, rec.Body.String(), tt.body)
		})
	}
}

func TestHealthRouteReturnsServiceUnavailableWhenPersistIsStale(t *testing.T) {
	a := newHTTPTestAgent(t)
	handler := a.routes(time.Minute)

	old := time.Now().Add(-2 * time.Minute)
	require.NoError(t, os.Chtimes(healthpkg.FilePath(), old, old))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), `"healthy":false`)
}
