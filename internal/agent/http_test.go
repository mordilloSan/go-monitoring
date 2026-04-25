package agent

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	healthpkg "github.com/mordilloSan/go-monitoring/internal/health"
	"github.com/mordilloSan/go-monitoring/internal/model/smart"
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
		{name: "summary", method: http.MethodGet, path: "/api/v1/summary", status: http.StatusOK, body: `"cpu_percent":55`},
		{name: "system history", method: http.MethodGet, path: "/api/v1/history/system?resolution=1m&from=0&to=9999999999999&limit=10", status: http.StatusOK, body: `"resolution":"1m"`},
		{name: "container history", method: http.MethodGet, path: "/api/v1/history/containers?resolution=1m&from=0&to=9999999999999&limit=10", status: http.StatusOK, body: `"cpu_percent":27.5`},
		{name: "containers", method: http.MethodGet, path: "/api/v1/containers", status: http.StatusOK, body: `"memory_mb":1`},
		{name: "systemd", method: http.MethodGet, path: "/api/v1/systemd", status: http.StatusOK, body: `"name":"nginx.service"`},
		{name: "smart", method: http.MethodGet, path: "/api/v1/smart", status: http.StatusOK, body: `"disk_name":"/dev/sdb"`},
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

func TestRequestLoggingEnabled(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		set   bool
		want  bool
		alias bool
	}{
		{name: "default", want: true},
		{name: "true", env: "true", set: true, want: true},
		{name: "one", env: "1", set: true, want: true},
		{name: "false", env: "false", set: true, want: false},
		{name: "zero", env: "0", set: true, want: false},
		{name: "alias false", env: "off", set: true, want: false, alias: true},
		{name: "invalid defaults true", env: "sometimes", set: true, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				if tt.alias {
					t.Setenv("REQUEST_LOG", tt.env)
				} else {
					t.Setenv("HTTP_LOG", tt.env)
				}
			}

			assert.Equal(t, tt.want, requestLoggingEnabled())
		})
	}
}

func TestLogRequestsWritesRequestLog(t *testing.T) {
	var buf bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(originalLogger) })

	handler := logRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("created"))
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/smart/refresh?force=true", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	logLine := buf.String()
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Contains(t, logLine, "msg=\"HTTP request\"")
	assert.Contains(t, logLine, "method=POST")
	assert.Contains(t, logLine, "path=\"/api/v1/smart/refresh?force=true\"")
	assert.Contains(t, logLine, "status=201")
	assert.Contains(t, logLine, "bytes=7")
	assert.NotContains(t, logLine, "remote=")
	assert.NotContains(t, logLine, "user_agent=")
}

func TestRoutesCanDisableRequestLogging(t *testing.T) {
	var buf bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(originalLogger) })

	a := newHTTPTestAgent(t)
	a.requestLogging = false
	handler := a.routes(time.Minute)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, buf.String(), "HTTP request")
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
