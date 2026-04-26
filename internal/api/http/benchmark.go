package httpapi

import (
	"net/http"
	"net/http/httptest"
	"time"

	apimodel "github.com/mordilloSan/go-monitoring/internal/api/model"
	"github.com/mordilloSan/go-monitoring/internal/store"
	"github.com/mordilloSan/go-monitoring/internal/utils"
)

type benchmarkEndpoint struct {
	method string
	path   string
}

func (s *Server) handleBenchmark(handler http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}

		start := time.Now()
		endpoints := s.benchmarkEndpoints()
		items := make([]apimodel.BenchmarkEndpointResult, 0, len(endpoints))
		successful := 0

		for _, endpoint := range endpoints {
			req := httptest.NewRequest(endpoint.method, endpoint.path, nil)
			rec := httptest.NewRecorder()

			callStart := time.Now()
			handler.ServeHTTP(rec, req)
			duration := time.Since(callStart)

			status := rec.Code
			if status == 0 {
				status = http.StatusOK
			}
			if status >= http.StatusOK && status < http.StatusBadRequest {
				successful++
			}

			items = append(items, apimodel.BenchmarkEndpointResult{
				Method:     endpoint.method,
				Path:       endpoint.path,
				Status:     status,
				DurationMS: utils.TwoDecimals(float64(duration.Nanoseconds()) / float64(time.Millisecond)),
				Bytes:      rec.Body.Len(),
			})
		}

		writeJSON(w, http.StatusOK, apimodel.BenchmarkResponse{
			CapturedAt:      time.Now().UTC().UnixMilli(),
			TotalDurationMS: utils.TwoDecimals(float64(time.Since(start).Nanoseconds()) / float64(time.Millisecond)),
			EndpointCount:   len(items),
			SuccessfulCount: successful,
			FailedCount:     len(items) - successful,
			Items:           items,
		})
	}
}

func (s *Server) benchmarkEndpoints() []benchmarkEndpoint {
	endpoints := []benchmarkEndpoint{
		{method: http.MethodGet, path: "/healthz"},
		{method: http.MethodGet, path: "/api/v1/meta"},
		{method: http.MethodGet, path: "/api/v1/system/summary"},
		{method: http.MethodGet, path: "/api/v1/plugins"},
	}

	for _, plugin := range store.PluginNames() {
		endpoints = append(endpoints, benchmarkEndpoint{
			method: http.MethodGet,
			path:   "/api/v1/" + plugin,
		})
		if s.metrics.HistoryEnabled(plugin) {
			endpoints = append(endpoints, benchmarkEndpoint{
				method: http.MethodGet,
				path:   "/api/v1/" + plugin + "/history?resolution=1m&limit=1",
			})
		}
	}

	endpoints = append(endpoints, benchmarkEndpoint{
		method: http.MethodGet,
		path:   "/api/v1/all",
	})

	return endpoints
}
