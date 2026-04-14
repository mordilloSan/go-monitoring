package agent

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/henrygd/beszel/pkg/agent/health"
)

const maxHistoryLimit = 1000

func (a *Agent) routes(collectorInterval time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", a.handleHealth)
	mux.HandleFunc("/api/v1/meta", a.handleMeta(collectorInterval))
	mux.HandleFunc("/api/v1/summary", a.handleSummary)
	mux.HandleFunc("/api/v1/history/system", a.handleSystemHistory)
	mux.HandleFunc("/api/v1/history/containers", a.handleContainerHistory)
	mux.HandleFunc("/api/v1/containers", a.handleContainers)
	mux.HandleFunc("/api/v1/systemd", a.handleSystemd)
	mux.HandleFunc("/api/v1/smart", a.handleSmart)
	mux.HandleFunc("/api/v1/smart/refresh", a.handleSmartRefresh)
	return mux
}

func (a *Agent) handleHealth(w http.ResponseWriter, r *http.Request) {
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

func (a *Agent) handleMeta(collectorInterval time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		writeJSON(w, http.StatusOK, defaultMetaResponse(a, collectorInterval))
	}
}

func (a *Agent) handleSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	summary, err := a.store.Summary()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (a *Agent) handleSystemHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	resolution, from, to, limit, ok := parseHistoryQuery(w, r)
	if !ok {
		return
	}
	history, err := a.store.SystemHistory(resolution, from, to, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, history)
}

func (a *Agent) handleContainerHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	resolution, from, to, limit, ok := parseHistoryQuery(w, r)
	if !ok {
		return
	}
	history, err := a.store.ContainerHistory(resolution, from, to, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, history)
}

func (a *Agent) handleContainers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	resp, err := a.store.CurrentContainers()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *Agent) handleSystemd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	resp, err := a.store.CurrentSystemd()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *Agent) handleSmart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	resp, err := a.store.CurrentSmartDevices()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *Agent) handleSmartRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}
	if err := a.RefreshSmartNow(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	resp, err := a.store.CurrentSmartDevices()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func parseHistoryQuery(w http.ResponseWriter, r *http.Request) (resolution string, from int64, to int64, limit int, ok bool) {
	query := r.URL.Query()
	resolution = query.Get("resolution")
	if !validResolution(resolution) {
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
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}
