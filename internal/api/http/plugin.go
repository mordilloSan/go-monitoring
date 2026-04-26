package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/mordilloSan/go-monitoring/internal/store"
)

type Plugin interface {
	Name() string
	Current() (any, error)
}

type HistoryPlugin interface {
	Plugin
	History(resolution string, from, to int64, limit int) (any, error)
}

type RefreshablePlugin interface {
	Plugin
	Refresh() error
}

type Registry struct {
	plugins []Plugin
	byName  map[string]Plugin
}

type pluginInfo struct {
	Name       string   `json:"name"`
	HasHistory bool     `json:"has_history"`
	HasRefresh bool     `json:"has_refresh"`
	Routes     []string `json:"routes"`
}

type rawDataResponse struct {
	CapturedAt int64           `json:"captured_at"`
	Data       json.RawMessage `json:"data"`
}

type rawItemsResponse struct {
	CapturedAt int64           `json:"captured_at"`
	Items      json.RawMessage `json:"items"`
}

type rawProcessesResponse struct {
	CapturedAt int64           `json:"captured_at"`
	Count      json.RawMessage `json:"count,omitempty"`
	Items      json.RawMessage `json:"items"`
}

type rawHistoryItem struct {
	CapturedAt int64           `json:"captured_at"`
	Stats      json.RawMessage `json:"stats"`
}

type rawHistoryResponse struct {
	Resolution string           `json:"resolution"`
	Items      []rawHistoryItem `json:"items"`
}

type currentPlugin struct {
	name          string
	kind          string
	current       CurrentReader
	historyReader MetricsReader
}

type historyCurrentPlugin struct {
	currentPlugin
}

type refreshCurrentPlugin struct {
	currentPlugin
	refresher SmartRefresher
}

type refreshHistoryCurrentPlugin struct {
	currentPlugin
	refresher SmartRefresher
}

func NewRegistry(current CurrentReader, metrics MetricsReader, refresher SmartRefresher) *Registry {
	registry := &Registry{byName: map[string]Plugin{}}
	for _, name := range store.PluginNames() {
		base := currentPlugin{
			name:          name,
			kind:          pluginResponseKind(name),
			current:       current,
			historyReader: metrics,
		}
		var plugin Plugin
		switch {
		case name == store.PluginSmart && metrics.HistoryEnabled(name) && refresher != nil:
			plugin = refreshHistoryCurrentPlugin{currentPlugin: base, refresher: refresher}
		case name == store.PluginSmart && refresher != nil:
			plugin = refreshCurrentPlugin{currentPlugin: base, refresher: refresher}
		case metrics.HistoryEnabled(name):
			plugin = historyCurrentPlugin{currentPlugin: base}
		default:
			plugin = base
		}
		registry.plugins = append(registry.plugins, plugin)
		registry.byName[name] = plugin
	}
	return registry
}

func (r *Registry) Mount(mux *http.ServeMux, prefix string) {
	mux.HandleFunc(prefix+"plugins", r.handlePlugins(prefix))
	mux.HandleFunc(prefix+"all", r.handleAll)
	mux.HandleFunc(prefix+"{plugin}", r.handleCurrent)
	mux.HandleFunc(prefix+"{plugin}/history", r.handleHistory)
	mux.HandleFunc(prefix+"{plugin}/refresh", r.handleRefresh)
}

func (r *Registry) handlePlugins(prefix string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			writeMethodNotAllowed(w, http.MethodGet)
			return
		}
		items := make([]pluginInfo, 0, len(r.plugins))
		for _, plugin := range r.plugins {
			name := plugin.Name()
			routes := []string{"GET " + prefix + name}
			_, hasHistory := plugin.(HistoryPlugin)
			if hasHistory {
				routes = append(routes, "GET "+prefix+name+"/history")
			}
			_, hasRefresh := plugin.(RefreshablePlugin)
			if hasRefresh {
				routes = append(routes, "POST "+prefix+name+"/refresh")
			}
			items = append(items, pluginInfo{
				Name:       name,
				HasHistory: hasHistory,
				HasRefresh: hasRefresh,
				Routes:     routes,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	}
}

func (r *Registry) handleAll(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	out := make(map[string]any, len(r.plugins))
	for _, plugin := range r.plugins {
		current, err := plugin.Current()
		if err != nil {
			writeStoreError(w, err)
			return
		}
		out[plugin.Name()] = current
	}
	writeJSON(w, http.StatusOK, out)
}

func (r *Registry) handleCurrent(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	plugin, ok := r.byName[req.PathValue("plugin")]
	if !ok {
		http.NotFound(w, req)
		return
	}
	current, err := plugin.Current()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, current)
}

func (r *Registry) handleHistory(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	plugin, ok := r.byName[req.PathValue("plugin")]
	if !ok {
		http.NotFound(w, req)
		return
	}
	historyPlugin, ok := plugin.(HistoryPlugin)
	if !ok {
		http.NotFound(w, req)
		return
	}
	resolution, from, to, limit, ok := parseHistoryQuery(w, req)
	if !ok {
		return
	}
	history, err := historyPlugin.History(resolution, from, to, limit)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, req)
			return
		}
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, history)
}

func (r *Registry) handleRefresh(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}
	plugin, ok := r.byName[req.PathValue("plugin")]
	if !ok {
		http.NotFound(w, req)
		return
	}
	refreshable, ok := plugin.(RefreshablePlugin)
	if !ok {
		http.NotFound(w, req)
		return
	}
	if err := refreshable.Refresh(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	current, err := plugin.Current()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, current)
}

func (p currentPlugin) Name() string {
	return p.name
}

func (p currentPlugin) Current() (any, error) {
	capturedAt, raw, err := p.current.CurrentPlugin(p.name)
	if err != nil {
		return nil, err
	}
	switch p.kind {
	case "items":
		return rawItemsResponse{CapturedAt: capturedAt, Items: normalizeItemsRaw(raw)}, nil
	case "processes":
		var payload struct {
			Count json.RawMessage `json:"count"`
			Items json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
		return rawProcessesResponse{
			CapturedAt: capturedAt,
			Count:      payload.Count,
			Items:      normalizeItemsRaw(payload.Items),
		}, nil
	default:
		return rawDataResponse{CapturedAt: capturedAt, Data: raw}, nil
	}
}

func (p historyCurrentPlugin) History(resolution string, from, to int64, limit int) (any, error) {
	return p.history(resolution, from, to, limit)
}

func (p refreshCurrentPlugin) Refresh() error {
	return p.refresher.RefreshSmartNow()
}

func (p refreshHistoryCurrentPlugin) Refresh() error {
	return p.refresher.RefreshSmartNow()
}

func (p refreshHistoryCurrentPlugin) History(resolution string, from, to int64, limit int) (any, error) {
	return p.history(resolution, from, to, limit)
}

func (p currentPlugin) history(resolution string, from, to int64, limit int) (any, error) {
	records, err := p.historyReader.PluginHistory(p.name, resolution, from, to, limit)
	if err != nil {
		return nil, err
	}
	items := make([]rawHistoryItem, 0, len(records))
	for _, record := range records {
		items = append(items, rawHistoryItem{
			CapturedAt: record.CapturedAt,
			Stats:      record.Stats,
		})
	}
	return rawHistoryResponse{
		Resolution: resolution,
		Items:      items,
	}, nil
}

func pluginResponseKind(name string) string {
	switch name {
	case store.PluginContainers, store.PluginSystemd, store.PluginPrograms, store.PluginIRQ, store.PluginSmart:
		return "items"
	case store.PluginProcesses:
		return "processes"
	default:
		return "data"
	}
}

func normalizeItemsRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage("[]")
	}
	return raw
}
