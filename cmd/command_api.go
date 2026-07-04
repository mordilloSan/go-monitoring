package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	httpapi "github.com/mordilloSan/go-monitoring/internal/api/http"
	apimodel "github.com/mordilloSan/go-monitoring/internal/api/model"
	"github.com/mordilloSan/go-monitoring/internal/app"
	"github.com/mordilloSan/go-monitoring/internal/config"
)

type agentCommandExecutor struct {
	app  *app.App
	opts cmdOptions
}

func newAgentCommandExecutor(agent *app.App, opts cmdOptions) *agentCommandExecutor {
	return &agentCommandExecutor{app: agent, opts: opts}
}

func (e *agentCommandExecutor) ExecuteCommand(ctx context.Context, req apimodel.CommandRequest) httpapi.CommandResult {
	req.Command = normalizeCommand(req.Command)
	if req.Command == "" {
		return commandError(req, http.StatusBadRequest, "invalid_command", "command is required")
	}
	switch req.Command {
	case "commands.list":
		return commandOK(req, commandList(), false)
	case "status.get":
		return commandOK(req, e.app.StatusMeta(), false)
	case "config.get":
		cfg, _, err := config.Load(e.opts.configPath)
		if err != nil {
			return commandError(req, http.StatusInternalServerError, "config_load_failed", err.Error())
		}
		return commandOK(req, cfg, false)
	case "config.set":
		return e.handleConfigSet(ctx, req)
	case "config.reload":
		return e.handleConfigReload(req)
	case "smart.refresh":
		if err := e.app.RefreshSmartNow(ctx); err != nil {
			return commandError(req, http.StatusInternalServerError, "smart_refresh_failed", err.Error())
		}
		return commandOK(req, map[string]any{"refreshed": true}, false)
	case "db.check":
		if err := e.app.CheckDatabase(); err != nil {
			return commandError(req, http.StatusInternalServerError, "db_check_failed", err.Error())
		}
		return commandOK(req, map[string]any{"path": e.app.StatusMeta().DBPath}, false)
	case "db.maintain":
		if err := e.app.MaintainDatabase(ctx); err != nil {
			return commandError(req, http.StatusInternalServerError, "db_maintain_failed", err.Error())
		}
		return commandOK(req, map[string]any{"path": e.app.StatusMeta().DBPath}, false)
	default:
		return commandError(req, http.StatusNotFound, "unknown_command", "unknown command")
	}
}

func normalizeCommand(command string) string {
	return strings.ToLower(strings.TrimSpace(command))
}

func commandList() []string {
	return []string{
		"commands.list",
		"status.get",
		"config.get",
		"config.set",
		"config.reload",
		"smart.refresh",
		"db.check",
		"db.maintain",
	}
}

func (e *agentCommandExecutor) handleConfigReload(req apimodel.CommandRequest) httpapi.CommandResult {
	effective, err := loadEffectiveConfig(e.opts)
	if err != nil {
		return commandError(req, http.StatusBadRequest, "invalid_config", err.Error())
	}
	return e.reloadRuntimeConfig(req, effective.cfg, effective.source)
}

type configSetParams struct {
	CollectorInterval   *string            `json:"collector_interval"`
	History             *string            `json:"history"`
	CacheTTL            map[string]string  `json:"cache_ttl"`
	Listeners           *[]config.Listener `json:"listeners"`
	AllowRemoteCommands *bool              `json:"allow_remote_commands"`
}

func (e *agentCommandExecutor) handleConfigSet(_ context.Context, req apimodel.CommandRequest) httpapi.CommandResult {
	if len(req.Params) == 0 {
		return commandError(req, http.StatusBadRequest, "invalid_params", "params are required")
	}
	var params configSetParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return commandError(req, http.StatusBadRequest, "invalid_params", err.Error())
	}
	cfg, _, err := config.Load(e.opts.configPath)
	if err != nil {
		return commandError(req, http.StatusInternalServerError, "config_load_failed", err.Error())
	}
	restartRequired, err := applyConfigSetParams(&cfg, params)
	if err != nil {
		return commandError(req, http.StatusBadRequest, "invalid_config", err.Error())
	}
	if err := config.Save(e.opts.configPath, cfg); err != nil {
		return commandError(req, http.StatusInternalServerError, "config_save_failed", err.Error())
	}
	reloadResult := e.reloadRuntimeConfig(req, cfg, "loaded")
	if !reloadResult.Response.OK {
		return reloadResult
	}
	reloadResult.Response.RestartRequired = reloadResult.Response.RestartRequired || restartRequired
	reloadResult.Response.Data = cfg
	return reloadResult
}

func applyConfigSetParams(cfg *config.Config, params configSetParams) (bool, error) {
	restartRequired := false
	if params.CollectorInterval != nil {
		parsed, err := time.ParseDuration(*params.CollectorInterval)
		if err != nil {
			return false, err
		}
		cfg.CollectorInterval = config.Duration(parsed)
	}
	if params.History != nil {
		cfg.History = *params.History
	}
	for key, raw := range params.CacheTTL {
		ttl, err := time.ParseDuration(raw)
		if err != nil {
			return false, err
		}
		if err := config.SetCacheTTL(cfg, key, ttl); err != nil {
			return false, err
		}
	}
	if params.Listeners != nil {
		cfg.Listeners = append([]config.Listener(nil), (*params.Listeners)...)
		restartRequired = true
	}
	if params.AllowRemoteCommands != nil {
		cfg.AllowRemoteCommands = *params.AllowRemoteCommands
	}
	if err := config.Validate(*cfg); err != nil {
		return false, err
	}
	return restartRequired, nil
}

func (e *agentCommandExecutor) reloadRuntimeConfig(req apimodel.CommandRequest, cfg config.Config, source string) httpapi.CommandResult {
	if err := e.app.ReloadRuntime(app.ReloadOptions{
		CollectorInterval: cfg.CollectorInterval.Duration(),
		History:           cfg.History,
		HistorySet:        true,
		CacheTTL:          config.ToDurationMap(cfg.CacheTTL),
		ConfigSource:      source,
		ConfigVersion:     cfg.Version,
	}); err != nil {
		return commandError(req, http.StatusBadRequest, "reload_failed", err.Error())
	}
	return commandOK(req, e.app.StatusMeta(), listenersRestartRequired(e.app.Listeners(), cfg))
}

func listenersRestartRequired(active []apimodel.ListenerMeta, cfg config.Config) bool {
	if len(active) != len(cfg.Listeners) {
		return true
	}
	for i, listener := range cfg.Listeners {
		if active[i].Name != listener.Name || active[i].Address != app.GetAddress(listener.Address) {
			return true
		}
	}
	return false
}

func commandOK(req apimodel.CommandRequest, data any, restartRequired bool) httpapi.CommandResult {
	return httpapi.CommandResult{
		Status: http.StatusOK,
		Response: apimodel.CommandResponse{
			OK:              true,
			Command:         req.Command,
			RequestID:       req.RequestID,
			RestartRequired: restartRequired,
			Data:            data,
		},
	}
}

func commandError(req apimodel.CommandRequest, status int, code, message string) httpapi.CommandResult {
	if status == 0 {
		status = http.StatusInternalServerError
	}
	if message == "" {
		message = code
	}
	return httpapi.CommandResult{
		Status: status,
		Response: apimodel.CommandResponse{
			OK:        false,
			Command:   req.Command,
			RequestID: req.RequestID,
			Error: &apimodel.CommandError{
				Code:    code,
				Message: message,
			},
		},
	}
}
