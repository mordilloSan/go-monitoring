// Package httpapi serves the go-monitoring REST API.
package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	apimodel "github.com/mordilloSan/go-monitoring/internal/api/model"
	"github.com/mordilloSan/go-monitoring/internal/domain/system"
	"github.com/mordilloSan/go-monitoring/internal/health"
	"github.com/mordilloSan/go-monitoring/internal/store"
	"github.com/mordilloSan/go-monitoring/internal/utils"
	"github.com/mordilloSan/go-monitoring/internal/version"
)

const maxHistoryLimit = 1000

type MetricsReader interface {
	Path() string
	CurrentPlugin(plugin string) (int64, json.RawMessage, error)
	PluginHistory(plugin, resolution string, from, to int64, limit int) ([]store.HistoryRecord[json.RawMessage], error)
	HistoryEnabled(plugin string) bool
	SystemSummary() (int64, system.Summary, error)
}

type SmartRefresher interface {
	RefreshSmartNow() error
}

type Options struct {
	Metrics              MetricsReader
	SmartRefresher       SmartRefresher
	DataDir              string
	ListenAddr           func() string
	SmartRefreshInterval func() string
	RequestLogging       bool
}

type Server struct {
	metrics              MetricsReader
	smartRefresher       SmartRefresher
	dataDir              string
	listenAddr           func() string
	smartRefreshInterval func() string
	requestLogging       bool
}

func NewServer(opts Options) *Server {
	return &Server{
		metrics:              opts.Metrics,
		smartRefresher:       opts.SmartRefresher,
		dataDir:              opts.DataDir,
		listenAddr:           opts.ListenAddr,
		smartRefreshInterval: opts.SmartRefreshInterval,
		requestLogging:       opts.RequestLogging,
	}
}

func (s *Server) Handler(collectorInterval time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/api/v1/meta", s.handleMeta(collectorInterval))
	mux.HandleFunc("/api/v1/system/summary", s.handleSystemSummary)
	NewRegistry(s.metrics, s.smartRefresher).Mount(mux, "/api/v1/")
	if s.requestLogging {
		return logRequests(mux)
	}
	return mux
}

func RequestLoggingEnabled() bool {
	value, exists := utils.GetEnv("HTTP_LOG")
	if !exists {
		value, exists = utils.GetEnv("REQUEST_LOG")
	}
	if !exists {
		return true
	}

	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		slog.Warn("Invalid HTTP_LOG value; defaulting to enabled", "value", value)
		return true
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status != 0 {
		return
	}
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(data)
	r.bytes += n
	return n, err
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}

		slog.Info("HTTP request",
			"method", r.Method,
			"path", r.URL.RequestURI(),
			"status", rec.status,
			"bytes", rec.bytes,
			"duration", time.Since(start),
		)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}

	status, err := health.GetStatus()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	code := http.StatusOK
	if !status.Healthy {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{
		"healthy":      status.Healthy,
		"last_updated": status.LastUpdated,
		"age_seconds":  status.Age.Seconds(),
	})
}

func (s *Server) handleMeta(collectorInterval time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		writeJSON(w, http.StatusOK, s.metaResponse(collectorInterval))
	}
}

func (s *Server) handleSystemSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}

	capturedAt, summary, err := s.metrics.SystemSummary()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apimodel.SystemSummaryResponse{
		CapturedAt: capturedAt,
		Summary:    summary,
	})
}

func (s *Server) metaResponse(collectorInterval time.Duration) apimodel.MetaResponse {
	smartRefreshInterval := ""
	if s.smartRefreshInterval != nil {
		smartRefreshInterval = s.smartRefreshInterval()
	}
	listenAddr := ""
	if s.listenAddr != nil {
		listenAddr = s.listenAddr()
	}
	return apimodel.MetaResponse{
		Version:              version.Version,
		DataDir:              s.dataDir,
		DBPath:               s.metrics.Path(),
		ListenAddr:           listenAddr,
		CollectorInterval:    collectorInterval.String(),
		SmartRefreshInterval: smartRefreshInterval,
		Retention:            store.RetentionStrings(),
	}
}

func parseHistoryQuery(w http.ResponseWriter, r *http.Request) (resolution string, from int64, to int64, limit int, ok bool) {
	query := r.URL.Query()
	resolution = query.Get("resolution")
	if !store.ValidResolution(resolution) {
		writeError(w, http.StatusBadRequest, errors.New("invalid resolution"))
		return "", 0, 0, 0, false
	}

	var err error
	from = 0
	if raw := query.Get("from"); raw != "" {
		from, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid from"))
			return "", 0, 0, 0, false
		}
	}

	to = time.Now().UTC().UnixMilli()
	if raw := query.Get("to"); raw != "" {
		to, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("invalid to"))
			return "", 0, 0, 0, false
		}
	}
	if from > to {
		writeError(w, http.StatusBadRequest, errors.New("from must be <= to"))
		return "", 0, 0, 0, false
	}

	limit = 100
	if raw := query.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit <= 0 || limit > maxHistoryLimit {
			writeError(w, http.StatusBadRequest, errors.New("invalid limit"))
			return "", 0, 0, 0, false
		}
	}
	return resolution, from, to, limit, true
}

func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}

func writeMethodNotAllowed(w http.ResponseWriter, method string) {
	w.Header().Set("Allow", method)
	writeError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, apimodel.ErrorResponse{Error: err.Error()})
}

func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}
