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
	"github.com/mordilloSan/go-monitoring/internal/domain/container"
	modelnet "github.com/mordilloSan/go-monitoring/internal/domain/network"
	procmodel "github.com/mordilloSan/go-monitoring/internal/domain/process"
	"github.com/mordilloSan/go-monitoring/internal/domain/system"
	"github.com/mordilloSan/go-monitoring/internal/domain/systemd"
	"github.com/mordilloSan/go-monitoring/internal/health"
	"github.com/mordilloSan/go-monitoring/internal/store"
	"github.com/mordilloSan/go-monitoring/internal/utils"
	"github.com/mordilloSan/go-monitoring/internal/version"
)

const maxHistoryLimit = 1000

type MetricsReader interface {
	Path() string
	Summary() (int64, *system.CombinedData, error)
	SystemHistory(resolution string, from, to int64, limit int) ([]store.HistoryRecord[system.Stats], error)
	ContainerHistory(resolution string, from, to int64, limit int) ([]store.HistoryRecord[[]container.Stats], error)
	CurrentContainers() (int64, []*container.Stats, error)
	CurrentSystemd() (int64, []*systemd.Service, error)
	CurrentProcesses() (int64, []procmodel.Process, error)
	CurrentProcessCount() (int64, procmodel.Count, error)
	CurrentPrograms() (int64, []procmodel.Program, error)
	CurrentConnections() (int64, modelnet.ConnectionStats, error)
	CurrentIRQ() (int64, []modelnet.IRQStat, error)
	CurrentSmartDevices() (int64, []store.SmartDeviceRecord, error)
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
	mux.HandleFunc("/api/v1/summary", s.handleSummary)
	mux.HandleFunc("/api/v1/history/system", s.handleSystemHistory)
	mux.HandleFunc("/api/v1/history/containers", s.handleContainerHistory)
	mux.HandleFunc("/api/v1/containers", s.handleContainers)
	mux.HandleFunc("/api/v1/systemd", s.handleSystemd)
	mux.HandleFunc("/api/v1/processlist", s.handleProcessList)
	mux.HandleFunc("/api/v1/processcount", s.handleProcessCount)
	mux.HandleFunc("/api/v1/programlist", s.handleProgramList)
	mux.HandleFunc("/api/v1/connections", s.handleConnections)
	mux.HandleFunc("/api/v1/irq", s.handleIRQ)
	mux.HandleFunc("/api/v1/smart", s.handleSmart)
	mux.HandleFunc("/api/v1/smart/refresh", s.handleSmartRefresh)
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

func (s *Server) metaResponse(collectorInterval time.Duration) apimodel.MetaResponse {
	smartRefreshInterval := ""
	if s.smartRefreshInterval != nil {
		smartRefreshInterval = s.smartRefreshInterval()
	}
	return apimodel.MetaResponse{
		Version:              version.Version,
		DataDir:              s.dataDir,
		DBPath:               s.metrics.Path(),
		ListenAddr:           s.listenAddr(),
		CollectorInterval:    collectorInterval.String(),
		SmartRefreshInterval: smartRefreshInterval,
		Retention:            store.RetentionStrings(),
	}
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	capturedAt, summary, err := s.metrics.Summary()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apimodel.SummaryResponse{
		CapturedAt:   capturedAt,
		CombinedData: *summary,
	})
}

func (s *Server) handleSystemHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	resolution, from, to, limit, ok := parseHistoryQuery(w, r)
	if !ok {
		return
	}
	items, err := s.metrics.SystemHistory(resolution, from, to, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	history := historyResponse(resolution, items)
	writeJSON(w, http.StatusOK, history)
}

func (s *Server) handleContainerHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	resolution, from, to, limit, ok := parseHistoryQuery(w, r)
	if !ok {
		return
	}
	items, err := s.metrics.ContainerHistory(resolution, from, to, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	history := historyResponse(resolution, items)
	writeJSON(w, http.StatusOK, history)
}

func (s *Server) handleContainers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	capturedAt, containers, err := s.metrics.CurrentContainers()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apimodel.CurrentItemsResponse[apimodel.ContainerCurrent]{
		CapturedAt: capturedAt,
		Items:      containerCurrentItems(containers),
	})
}

func (s *Server) handleSystemd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	capturedAt, items, err := s.metrics.CurrentSystemd()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, currentItemsResponse(capturedAt, items))
}

func (s *Server) handleProcessList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	capturedAt, items, err := s.metrics.CurrentProcesses()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, currentItemsResponse(capturedAt, items))
}

func (s *Server) handleProcessCount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	capturedAt, data, err := s.metrics.CurrentProcessCount()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, currentDataResponse(capturedAt, data))
}

func (s *Server) handleProgramList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	capturedAt, items, err := s.metrics.CurrentPrograms()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, currentItemsResponse(capturedAt, items))
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	capturedAt, data, err := s.metrics.CurrentConnections()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, currentDataResponse(capturedAt, data))
}

func (s *Server) handleIRQ(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	capturedAt, items, err := s.metrics.CurrentIRQ()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, currentItemsResponse(capturedAt, items))
}

func (s *Server) handleSmart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	capturedAt, items, err := s.metrics.CurrentSmartDevices()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apimodel.CurrentItemsResponse[apimodel.SmartDeviceCurrent]{
		CapturedAt: capturedAt,
		Items:      smartDeviceCurrentItems(items),
	})
}

func (s *Server) handleSmartRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}
	if err := s.smartRefresher.RefreshSmartNow(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	capturedAt, items, err := s.metrics.CurrentSmartDevices()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, apimodel.CurrentItemsResponse[apimodel.SmartDeviceCurrent]{
		CapturedAt: capturedAt,
		Items:      smartDeviceCurrentItems(items),
	})
}

func historyResponse[T any](resolution string, records []store.HistoryRecord[T]) apimodel.HistoryResponse[T] {
	items := make([]apimodel.HistoryItem[T], 0, len(records))
	for _, record := range records {
		items = append(items, apimodel.HistoryItem[T]{
			CapturedAt: record.CapturedAt,
			Stats:      record.Stats,
		})
	}
	return apimodel.HistoryResponse[T]{
		Resolution: resolution,
		Items:      items,
	}
}

func currentItemsResponse[T any](capturedAt int64, items []T) apimodel.CurrentItemsResponse[T] {
	return apimodel.CurrentItemsResponse[T]{
		CapturedAt: capturedAt,
		Items:      items,
	}
}

func currentDataResponse[T any](capturedAt int64, data T) apimodel.CurrentDataResponse[T] {
	return apimodel.CurrentDataResponse[T]{
		CapturedAt: capturedAt,
		Data:       data,
	}
}

func containerCurrentItems(items []*container.Stats) []apimodel.ContainerCurrent {
	out := make([]apimodel.ContainerCurrent, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		out = append(out, apimodel.ContainerCurrent{
			ID:          item.Id,
			Name:        item.Name,
			Image:       item.Image,
			Ports:       item.Ports,
			Status:      item.Status,
			Health:      item.Health,
			Cpu:         item.Cpu,
			Mem:         item.Mem,
			NetworkSent: item.NetworkSent,
			NetworkRecv: item.NetworkRecv,
			Bandwidth:   item.Bandwidth,
		})
	}
	return out
}

func smartDeviceCurrentItems(items []store.SmartDeviceRecord) []apimodel.SmartDeviceCurrent {
	out := make([]apimodel.SmartDeviceCurrent, 0, len(items))
	for _, item := range items {
		out = append(out, apimodel.SmartDeviceCurrent{
			ID:   item.ID,
			Key:  item.Key,
			Data: item.Data,
		})
	}
	return out
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
