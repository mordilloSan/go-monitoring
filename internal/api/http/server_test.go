package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apimodel "github.com/mordilloSan/go-monitoring/internal/api/model"
	"github.com/mordilloSan/go-monitoring/internal/domain/smart"
	"github.com/mordilloSan/go-monitoring/internal/domain/system"
	healthpkg "github.com/mordilloSan/go-monitoring/internal/health"
	storepkg "github.com/mordilloSan/go-monitoring/internal/store"
	"github.com/mordilloSan/go-monitoring/internal/store/storetest"
)

type fakeSmartRefresher struct {
	store *storepkg.Store
}

func (r fakeSmartRefresher) RefreshSmartNow(context.Context) error {
	return r.store.WriteSmartDevices(time.Now().UTC().UnixMilli(), map[string]smart.SmartData{})
}

type fakeCurrentReader struct {
	capturedAt   int64
	plugins      map[string]json.RawMessage
	pluginErrors map[string]error
	summary      system.Summary
	summaryErr   error
}

func (r fakeCurrentReader) CurrentPlugin(_ context.Context, plugin string) (int64, json.RawMessage, error) {
	if err := r.pluginErrors[plugin]; err != nil {
		return 0, nil, err
	}
	if raw, ok := r.plugins[plugin]; ok {
		return r.capturedAt, raw, nil
	}
	return r.capturedAt, json.RawMessage(`{}`), nil
}

func (r fakeCurrentReader) SystemSummary(context.Context) (int64, system.Summary, error) {
	if r.summaryErr != nil {
		return 0, system.Summary{}, r.summaryErr
	}
	return r.capturedAt, r.summary, nil
}

func newHTTPTestServer(t *testing.T) *Server {
	t.Helper()

	tmpDir := t.TempDir()
	store, err := storepkg.OpenStore(tmpDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	capturedAt := time.Now().UTC().UnixMilli()
	require.NoError(t, store.WriteSnapshot(capturedAt, storetest.SampleCombinedData(55)))
	require.NoError(t, store.WriteSmartDevices(capturedAt, map[string]smart.SmartData{
		"/dev/sdb": {
			ModelName:   "disk-b",
			DiskName:    "/dev/sdb",
			SmartStatus: "passed",
		},
	}))
	require.NoError(t, healthpkg.Update())
	t.Cleanup(func() { _ = healthpkg.CleanUp() })

	return NewServer(Options{
		Metrics:              store,
		Current:              store,
		SmartRefresher:       fakeSmartRefresher{store: store},
		DataDir:              tmpDir,
		ListenAddr:           func() string { return ":45876" },
		SmartRefreshInterval: func() string { return "" },
		RequestLogging:       true,
	})
}

func TestCurrentRoutesUseCurrentReaderAndHistoryUsesStore(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storepkg.OpenStore(tmpDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	capturedAt := time.Now().UTC().UnixMilli()
	require.NoError(t, store.WriteSnapshot(capturedAt, storetest.SampleCombinedData(55)))

	server := NewServer(Options{
		Metrics: store,
		Current: fakeCurrentReader{
			capturedAt: 123,
			plugins: map[string]json.RawMessage{
				storepkg.PluginCPU: json.RawMessage(`{"cpu_percent":99}`),
			},
			summary: system.Summary{Hostname: "live-host", CPUPercent: 99},
		},
		SmartRefresher: fakeSmartRefresher{store: store},
		DataDir:        tmpDir,
	})
	handler := server.Handler(func() time.Duration { return time.Minute })

	cpuReq := httptest.NewRequest(http.MethodGet, "/api/v1/cpu", nil)
	cpuRec := httptest.NewRecorder()
	handler.ServeHTTP(cpuRec, cpuReq)
	require.Equal(t, http.StatusOK, cpuRec.Code)
	assert.Contains(t, cpuRec.Body.String(), `"cpu_percent":99`)
	assert.NotContains(t, cpuRec.Body.String(), `"cpu_percent":55`)

	summaryReq := httptest.NewRequest(http.MethodGet, "/api/v1/system/summary", nil)
	summaryRec := httptest.NewRecorder()
	handler.ServeHTTP(summaryRec, summaryReq)
	require.Equal(t, http.StatusOK, summaryRec.Code)
	assert.Contains(t, summaryRec.Body.String(), `"hostname":"live-host"`)

	historyReq := httptest.NewRequest(http.MethodGet, "/api/v1/cpu/history?resolution=1m&from=0&to=9999999999999&limit=10", nil)
	historyRec := httptest.NewRecorder()
	handler.ServeHTTP(historyRec, historyReq)
	require.Equal(t, http.StatusOK, historyRec.Code)
	assert.Contains(t, historyRec.Body.String(), `"cpu_percent":55`)
}

func TestHistoryRoutesReflectUpdatedHistoryAllowlist(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storepkg.OpenStore(tmpDir, storepkg.Options{HistoryPlugins: []string{storepkg.PluginCPU}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	capturedAt := time.Now().UTC().UnixMilli()
	require.NoError(t, store.WriteSnapshot(capturedAt, storetest.SampleCombinedData(55)))

	server := NewServer(Options{
		Metrics: store,
		Current: store,
		DataDir: tmpDir,
	})
	handler := server.Handler(func() time.Duration { return time.Minute })

	pluginsReq := httptest.NewRequest(http.MethodGet, "/api/v1/plugins", nil)
	pluginsRec := httptest.NewRecorder()
	handler.ServeHTTP(pluginsRec, pluginsReq)
	require.Equal(t, http.StatusOK, pluginsRec.Code)
	assert.Contains(t, pluginsRec.Body.String(), `"name":"swap","has_history":false`)

	disabledReq := httptest.NewRequest(http.MethodGet, "/api/v1/swap/history?resolution=1m", nil)
	disabledRec := httptest.NewRecorder()
	handler.ServeHTTP(disabledRec, disabledReq)
	require.Equal(t, http.StatusNotFound, disabledRec.Code)

	store.SetHistoryPlugins([]string{storepkg.PluginCPU, storepkg.PluginSwap})
	require.NoError(t, store.WriteSnapshot(capturedAt+1, storetest.SampleCombinedData(60)))

	pluginsReq = httptest.NewRequest(http.MethodGet, "/api/v1/plugins", nil)
	pluginsRec = httptest.NewRecorder()
	handler.ServeHTTP(pluginsRec, pluginsReq)
	require.Equal(t, http.StatusOK, pluginsRec.Code)
	assert.Contains(t, pluginsRec.Body.String(), `"name":"swap","has_history":true`)

	enabledReq := httptest.NewRequest(http.MethodGet, "/api/v1/swap/history?resolution=1m&from=0&to=9999999999999&limit=10", nil)
	enabledRec := httptest.NewRecorder()
	handler.ServeHTTP(enabledRec, enabledReq)
	require.Equal(t, http.StatusOK, enabledRec.Code)
	assert.Contains(t, enabledRec.Body.String(), `"resolution":"1m"`)
}

func TestHistoryQueryValidationAndLimit(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storepkg.OpenStore(tmpDir, storepkg.Options{HistoryPlugins: []string{storepkg.PluginCPU}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	start := time.Now().UTC().Truncate(time.Millisecond)
	for i := range 3 {
		require.NoError(t, store.WriteSnapshot(start.Add(time.Duration(i)*time.Millisecond).UnixMilli(), storetest.SampleCombinedData(float64(i+1))))
	}

	server := NewServer(Options{Metrics: store, Current: store, DataDir: tmpDir})
	handler := server.Handler(func() time.Duration { return time.Minute })

	tests := []struct {
		name   string
		method string
		path   string
		status int
		body   string
	}{
		{name: "negative limit", method: http.MethodGet, path: "/api/v1/cpu/history?resolution=1m&limit=-1", status: http.StatusBadRequest, body: `"error":"invalid limit"`},
		{name: "oversized limit", method: http.MethodGet, path: "/api/v1/cpu/history?resolution=1m&limit=1001", status: http.StatusBadRequest, body: `"error":"invalid limit"`},
		{name: "from after to", method: http.MethodGet, path: "/api/v1/cpu/history?resolution=1m&from=20&to=10", status: http.StatusBadRequest, body: `"error":"from must be \u003c= to"`},
		{name: "unknown plugin", method: http.MethodGet, path: "/api/v1/nope/history?resolution=1m", status: http.StatusNotFound, body: "404 page not found"},
		{name: "method not allowed", method: http.MethodPost, path: "/api/v1/cpu/history?resolution=1m", status: http.StatusMethodNotAllowed, body: `"error":"method not allowed"`},
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

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cpu/history?resolution=1m&from=0&to=9999999999999&limit=2", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var response rawHistoryResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	assert.Len(t, response.Items, 2)
}

func TestEmptyStoreCurrentRouteReturnsNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storepkg.OpenStore(tmpDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(Options{Metrics: store, Current: store, DataDir: tmpDir})
	handler := server.Handler(func() time.Duration { return time.Minute })

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cpu", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), `"error":"not found"`)
}

func TestInternalErrorsUseGenericBodies(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storepkg.OpenStore(tmpDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(Options{
		Metrics: store,
		Current: fakeCurrentReader{
			pluginErrors: map[string]error{
				storepkg.PluginCPU: errors.New("secret database path leaked"),
			},
		},
		DataDir: tmpDir,
	})
	handler := server.Handler(func() time.Duration { return time.Minute })

	req := httptest.NewRequest(http.MethodGet, "/api/v1/cpu", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), `"error":"internal server error"`)
	assert.NotContains(t, rec.Body.String(), "secret")
}

func TestAllRouteReturnsPartialDataAndPluginErrors(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := storepkg.OpenStore(tmpDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	server := NewServer(Options{
		Metrics: store,
		Current: fakeCurrentReader{
			capturedAt: 123,
			plugins: map[string]json.RawMessage{
				storepkg.PluginMem: json.RawMessage(`{"memory_gb":16}`),
			},
			pluginErrors: map[string]error{
				storepkg.PluginCPU: errors.New("private failure details"),
			},
		},
		DataDir: tmpDir,
	})
	handler := server.Handler(func() time.Duration { return time.Minute })

	req := httptest.NewRequest(http.MethodGet, "/api/v1/all", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `"mem"`)
	assert.Contains(t, body, `"errors"`)
	assert.Contains(t, body, `"cpu":"internal server error"`)
	assert.NotContains(t, body, "private failure")
}

func TestHTTPRoutes(t *testing.T) {
	server := newHTTPTestServer(t)
	handler := server.Handler(func() time.Duration { return time.Minute })

	tests := []struct {
		name   string
		method string
		path   string
		status int
		body   string
	}{
		{name: "health", method: http.MethodGet, path: "/healthz", status: http.StatusOK, body: `"healthy":true`},
		{name: "meta", method: http.MethodGet, path: "/api/v1/meta", status: http.StatusOK, body: `"collector_interval":"1m0s"`},
		{name: "system summary", method: http.MethodGet, path: "/api/v1/system/summary", status: http.StatusOK, body: `"hostname":"host-a"`},
		{name: "plugins", method: http.MethodGet, path: "/api/v1/plugins", status: http.StatusOK, body: `"name":"cpu"`},
		{name: "all", method: http.MethodGet, path: "/api/v1/all", status: http.StatusOK, body: `"containers"`},
		{name: "cpu", method: http.MethodGet, path: "/api/v1/cpu", status: http.StatusOK, body: `"cpu_percent":55`},
		{name: "mem", method: http.MethodGet, path: "/api/v1/mem", status: http.StatusOK, body: `"memory_gb":16`},
		{name: "swap", method: http.MethodGet, path: "/api/v1/swap", status: http.StatusOK, body: `"data":{}`},
		{name: "load", method: http.MethodGet, path: "/api/v1/load", status: http.StatusOK, body: `"load_average":[1,2,3]`},
		{name: "diskio", method: http.MethodGet, path: "/api/v1/diskio", status: http.StatusOK, body: `"disk_total_gb":100`},
		{name: "fs", method: http.MethodGet, path: "/api/v1/fs", status: http.StatusOK, body: `"data":{}`},
		{name: "network", method: http.MethodGet, path: "/api/v1/network", status: http.StatusOK, body: `"bandwidth_bytes_per_second":[1000,2000]`},
		{name: "gpu", method: http.MethodGet, path: "/api/v1/gpu", status: http.StatusOK, body: `"data":{}`},
		{name: "sensors", method: http.MethodGet, path: "/api/v1/sensors", status: http.StatusOK, body: `"data":{}`},
		{name: "containers", method: http.MethodGet, path: "/api/v1/containers", status: http.StatusOK, body: `"image":"nginx"`},
		{name: "systemd", method: http.MethodGet, path: "/api/v1/systemd", status: http.StatusOK, body: `"name":"nginx.service"`},
		{name: "processes", method: http.MethodGet, path: "/api/v1/processes", status: http.StatusOK, body: `"total":2`},
		{name: "programs", method: http.MethodGet, path: "/api/v1/programs", status: http.StatusOK, body: `"name":"nginx"`},
		{name: "connections", method: http.MethodGet, path: "/api/v1/connections", status: http.StatusOK, body: `"nf_conntrack_count":7`},
		{name: "irq", method: http.MethodGet, path: "/api/v1/irq", status: http.StatusOK, body: `"irq":"0"`},
		{name: "smart", method: http.MethodGet, path: "/api/v1/smart", status: http.StatusOK, body: `"disk_name":"/dev/sdb"`},
		{name: "cpu history", method: http.MethodGet, path: "/api/v1/cpu/history?resolution=1m&from=0&to=9999999999999&limit=10", status: http.StatusOK, body: `"resolution":"1m"`},
		{name: "container history", method: http.MethodGet, path: "/api/v1/containers/history?resolution=1m&from=0&to=9999999999999&limit=10", status: http.StatusOK, body: `"cpu_percent":27.5`},
		{name: "disabled history", method: http.MethodGet, path: "/api/v1/swap/history?resolution=1m", status: http.StatusNotFound, body: "404 page not found"},
		{name: "invalid history", method: http.MethodGet, path: "/api/v1/cpu/history?resolution=bad", status: http.StatusBadRequest, body: `"error":"invalid resolution"`},
		{name: "smart refresh", method: http.MethodPost, path: "/api/v1/smart/refresh", status: http.StatusOK, body: `"items":[]`},
		{name: "benchmark", method: http.MethodGet, path: "/api/v1/benchmark", status: http.StatusOK, body: `"endpoint_count":`},
		{name: "legacy summary removed", method: http.MethodGet, path: "/api/v1/summary", status: http.StatusNotFound, body: "404 page not found"},
		{name: "legacy system history removed", method: http.MethodGet, path: "/api/v1/history/system?resolution=1m", status: http.StatusNotFound, body: "404 page not found"},
		{name: "legacy processlist removed", method: http.MethodGet, path: "/api/v1/processlist", status: http.StatusNotFound, body: "404 page not found"},
		{name: "legacy processcount removed", method: http.MethodGet, path: "/api/v1/processcount", status: http.StatusNotFound, body: "404 page not found"},
		{name: "legacy programlist removed", method: http.MethodGet, path: "/api/v1/programlist", status: http.StatusNotFound, body: "404 page not found"},
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

func TestBenchmarkEndpointReportsReadOnlyRoutes(t *testing.T) {
	server := newHTTPTestServer(t)
	handler := server.Handler(func() time.Duration { return time.Minute })
	req := httptest.NewRequest(http.MethodGet, "/api/v1/benchmark", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var response apimodel.BenchmarkResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	expectedEndpointCount := 4 + len(storepkg.PluginNames()) + len(storepkg.DefaultHistoryPluginNames()) + 1
	assert.Equal(t, expectedEndpointCount, response.EndpointCount)
	assert.Len(t, response.Items, response.EndpointCount)
	assert.Equal(t, response.EndpointCount, response.SuccessfulCount)
	assert.Zero(t, response.FailedCount)

	paths := map[string]string{}
	for _, item := range response.Items {
		paths[item.Method+" "+item.Path] = item.Method
		assert.NotZero(t, item.Status)
	}
	assert.Contains(t, paths, "GET /api/v1/system/summary")
	assert.Contains(t, paths, "GET /api/v1/cpu/history?resolution=1m&limit=1")
	assert.NotContains(t, paths, "POST /api/v1/smart/refresh")
	require.NotEmpty(t, response.Items)
	assert.Equal(t, "/api/v1/all", response.Items[len(response.Items)-1].Path)
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

			assert.Equal(t, tt.want, RequestLoggingEnabled())
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

	server := newHTTPTestServer(t)
	server.requestLogging = false
	handler := server.Handler(func() time.Duration { return time.Minute })

	req := httptest.NewRequest(http.MethodGet, "/api/v1/meta", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, buf.String(), "HTTP request")
}

func TestHealthRouteReturnsServiceUnavailableWhenPersistIsStale(t *testing.T) {
	server := newHTTPTestServer(t)
	handler := server.Handler(func() time.Duration { return time.Minute })

	old := time.Now().Add(-2 * time.Minute)
	require.NoError(t, os.Chtimes(healthpkg.FilePath(), old, old))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), `"healthy":false`)
}
