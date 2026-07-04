package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mordilloSan/go-monitoring/internal/app"
	"github.com/mordilloSan/go-monitoring/internal/store"
)

const (
	CurrentVersion = 1
	EnvConfigFile  = "CONFIG_FILE"
)

const (
	APIKindMetrics  = "metrics"
	APIKindCommands = "commands"
)

type Duration time.Duration

func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	if parsed < 0 {
		return fmt.Errorf("duration must not be negative")
	}
	*d = Duration(parsed)
	return nil
}

type Config struct {
	Version             int                 `json:"version"`
	Listeners           []Listener          `json:"listeners"`
	AllowRemoteCommands bool                `json:"allow_remote_commands,omitempty"`
	CollectorInterval   Duration            `json:"collector_interval"`
	History             string              `json:"history"`
	CacheTTL            map[string]Duration `json:"cache_ttl"`
}

type Listener struct {
	Name     string   `json:"name"`
	Address  string   `json:"address"`
	APIs     []string `json:"apis"`
	Implicit bool     `json:"-"`
}

func DefaultPath() string {
	if path := strings.TrimSpace(os.Getenv(EnvConfigFile)); path != "" {
		return path
	}
	if os.Geteuid() == 0 {
		return "/etc/go-monitoring/config.json"
	}
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, "go-monitoring", "config.json")
	}
	return "go-monitoring.json"
}

func Default() Config {
	ttls := app.DefaultLiveCurrentTTLs()
	cacheTTL := make(map[string]Duration, len(ttls))
	for key, ttl := range ttls {
		cacheTTL[key] = Duration(ttl)
	}
	return Config{
		Version:           CurrentVersion,
		Listeners:         DefaultListeners(),
		CollectorInterval: Duration(app.DefaultCollectorInterval),
		History:           strings.Join(store.DefaultHistoryPluginNames(), ","),
		CacheTTL:          cacheTTL,
	}
}

func DefaultListeners() []Listener {
	return []Listener{
		{Name: APIKindMetrics, Address: "127.0.0.1:45876", APIs: []string{APIKindMetrics}, Implicit: true},
		{Name: "control", Address: "unix:" + DefaultCommandSocketPath(), APIs: []string{APIKindCommands}, Implicit: true},
	}
}

func DefaultCommandSocketPath() string {
	if os.Geteuid() == 0 {
		return "/run/go-monitoring/agent.sock"
	}
	if runtimeDir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); runtimeDir != "" {
		return filepath.Join(runtimeDir, "go-monitoring", "agent.sock")
	}
	if cacheDir, err := os.UserCacheDir(); err == nil && cacheDir != "" {
		return filepath.Join(cacheDir, "go-monitoring", "agent.sock")
	}
	return filepath.Join(os.TempDir(), "go-monitoring", "agent.sock")
}

func Load(path string) (Config, bool, error) {
	cfg := Default()
	if strings.TrimSpace(path) == "" {
		path = DefaultPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, false, nil
		}
		return cfg, false, err
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, true, err
	}
	normalizeLoadedConfig(&cfg)
	for key := range cfg.CacheTTL {
		if err := validateCacheKey(key); err != nil {
			return cfg, true, err
		}
	}
	return cfg, true, nil
}

func Save(path string, cfg Config) error {
	if strings.TrimSpace(path) == "" {
		path = DefaultPath()
	}
	if err := Validate(cfg); err != nil {
		return err
	}
	if cfg.Listeners == nil {
		cfg.Listeners = []Listener{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func SaveIfMissing(path string, cfg Config) (bool, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultPath()
	}
	if err := Validate(cfg); err != nil {
		return false, err
	}
	if cfg.Listeners == nil {
		cfg.Listeners = []Listener{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return false, err
	}
	data = append(data, '\n')

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	if _, err := file.Write(data); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			return false, errors.Join(err, closeErr)
		}
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, err
	}
	return true, nil
}

func Validate(cfg Config) error {
	if cfg.Version != CurrentVersion {
		return fmt.Errorf("unsupported config version %d", cfg.Version)
	}
	if cfg.CollectorInterval.Duration() <= 0 {
		return fmt.Errorf("collector_interval must be greater than zero")
	}
	if err := validateListeners(cfg); err != nil {
		return err
	}
	for key, ttl := range cfg.CacheTTL {
		if err := validateCacheKey(key); err != nil {
			return err
		}
		if ttl.Duration() < 0 {
			return fmt.Errorf("cache_ttl.%s must not be negative", key)
		}
	}
	if _, err := store.ParseHistoryPlugins(cfg.History, true); err != nil {
		return err
	}
	return nil
}

func ApplyEnv(cfg *Config, env func(string) (string, bool)) {
	if value, ok := env("HISTORY"); ok {
		cfg.History = value
	}

	ttls := ToDurationMap(cfg.CacheTTL)
	app.ApplyLiveCurrentTTLEnv(ttls, env)
	cfg.CacheTTL = FromDurationMap(ttls)
}

func normalizeLoadedConfig(cfg *Config) {
	for i := range cfg.Listeners {
		cfg.Listeners[i].Name = strings.TrimSpace(cfg.Listeners[i].Name)
		cfg.Listeners[i].Address = strings.TrimSpace(cfg.Listeners[i].Address)
		cfg.Listeners[i].APIs = normalizeAPIs(cfg.Listeners[i].APIs)
	}
}

func SetMetricsListener(cfg *Config, listen string) {
	normalized := app.GetAddress(listen)
	if app.IsListenDisabled(normalized) {
		out := cfg.Listeners[:0]
		for _, listener := range cfg.Listeners {
			if !listenerHasAPI(listener, APIKindMetrics) {
				out = append(out, listener)
			}
		}
		cfg.Listeners = out
		return
	}
	listener := Listener{Name: APIKindMetrics, Address: normalized, APIs: []string{APIKindMetrics}}
	for i := range cfg.Listeners {
		if listenerHasAPI(cfg.Listeners[i], APIKindMetrics) {
			cfg.Listeners[i].Address = normalized
			cfg.Listeners[i].APIs = ensureAPI(cfg.Listeners[i].APIs, APIKindMetrics)
			return
		}
	}
	cfg.Listeners = append([]Listener{listener}, cfg.Listeners...)
}

func MetricsListener(cfg Config) (Listener, bool) {
	for _, listener := range cfg.Listeners {
		if listenerHasAPI(listener, APIKindMetrics) {
			return listener, true
		}
	}
	return Listener{}, false
}

func CommandListener(cfg Config) (Listener, bool) {
	for _, listener := range cfg.Listeners {
		if listenerHasAPI(listener, APIKindCommands) {
			return listener, true
		}
	}
	return Listener{}, false
}

func listenerHasAPI(listener Listener, api string) bool {
	for _, value := range listener.APIs {
		if strings.EqualFold(strings.TrimSpace(value), api) {
			return true
		}
	}
	return false
}

func ensureAPI(apis []string, api string) []string {
	for _, value := range apis {
		if strings.EqualFold(strings.TrimSpace(value), api) {
			return normalizeAPIs(apis)
		}
	}
	return append(normalizeAPIs(apis), api)
}

func normalizeAPIs(apis []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(apis))
	for _, api := range apis {
		api = strings.ToLower(strings.TrimSpace(api))
		if api == "" || seen[api] {
			continue
		}
		seen[api] = true
		out = append(out, api)
	}
	return out
}

func validateListeners(cfg Config) error {
	seenNames := map[string]bool{}
	seenAddresses := map[string]bool{}
	for i, listener := range cfg.Listeners {
		if err := validateListenerName(i, listener, seenNames); err != nil {
			return err
		}
		if err := validateListenerAddress(i, listener, seenAddresses); err != nil {
			return err
		}
		if err := validateListenerAPIs(i, listener, cfg.AllowRemoteCommands); err != nil {
			return err
		}
	}
	return nil
}

func validateListenerName(index int, listener Listener, seen map[string]bool) error {
	if strings.TrimSpace(listener.Name) == "" {
		return fmt.Errorf("listeners[%d].name cannot be empty", index)
	}
	name := strings.ToLower(strings.TrimSpace(listener.Name))
	if seen[name] {
		return fmt.Errorf("duplicate listener name %q", listener.Name)
	}
	seen[name] = true
	return nil
}

func validateListenerAddress(index int, listener Listener, seen map[string]bool) error {
	address := strings.TrimSpace(listener.Address)
	if address == "" {
		return fmt.Errorf("listeners[%d].address cannot be empty", index)
	}
	if app.IsListenDisabled(address) {
		return fmt.Errorf("listeners[%d].address cannot be disabled", index)
	}
	normalizedAddress := normalizeListenerAddress(address)
	if seen[normalizedAddress] {
		return fmt.Errorf("duplicate listener address %q", address)
	}
	seen[normalizedAddress] = true
	return nil
}

func validateListenerAPIs(index int, listener Listener, allowRemoteCommands bool) error {
	if len(listener.APIs) == 0 {
		return fmt.Errorf("listeners[%d].apis cannot be empty", index)
	}
	for _, api := range listener.APIs {
		switch strings.ToLower(strings.TrimSpace(api)) {
		case APIKindMetrics, APIKindCommands:
		default:
			return fmt.Errorf("listeners[%d].apis contains unknown API %q", index, api)
		}
	}
	if listenerHasAPI(listener, APIKindCommands) && !allowRemoteCommands && listenerIsNonLoopbackTCP(listener.Address) {
		return fmt.Errorf("listeners[%d] enables commands on non-loopback TCP; set allow_remote_commands to true", index)
	}
	return nil
}

func normalizeListenerAddress(address string) string {
	normalized := app.GetAddress(address)
	network, addr := app.SplitListenAddress(normalized)
	if network == "unix" {
		return "unix:" + addr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "tcp:" + addr
	}
	return "tcp:" + net.JoinHostPort(host, port)
}

func listenerIsNonLoopbackTCP(address string) bool {
	normalized := app.GetAddress(address)
	network, addr := app.SplitListenAddress(normalized)
	if network != "tcp" {
		return false
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return true
	}
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "localhost", "127.0.0.1", "::1":
		return false
	case "", "0.0.0.0", "::":
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip == nil || !ip.IsLoopback()
}

func ApplyCacheDefault(cfg *Config, ttl time.Duration) {
	for key := range cfg.CacheTTL {
		cfg.CacheTTL[key] = Duration(ttl)
	}
}

func ApplyCacheExpensive(cfg *Config, ttl time.Duration) {
	for _, key := range []string{
		store.PluginContainers,
		store.PluginSystemd,
		store.PluginProcesses,
		store.PluginPrograms,
		store.PluginConnections,
		store.PluginSmart,
	} {
		cfg.CacheTTL[key] = Duration(ttl)
	}
}

func SetCacheTTL(cfg *Config, key string, ttl time.Duration) error {
	key = strings.ToLower(strings.TrimSpace(key))
	if err := validateCacheKey(key); err != nil {
		return err
	}
	if ttl < 0 {
		return fmt.Errorf("cache TTL must not be negative")
	}
	cfg.CacheTTL[key] = Duration(ttl)
	return nil
}

func ToDurationMap(cacheTTL map[string]Duration) map[string]time.Duration {
	out := make(map[string]time.Duration, len(cacheTTL))
	for key, ttl := range cacheTTL {
		out[key] = ttl.Duration()
	}
	return out
}

func FromDurationMap(ttls map[string]time.Duration) map[string]Duration {
	out := make(map[string]Duration, len(ttls))
	for key, ttl := range ttls {
		out[key] = Duration(ttl)
	}
	return out
}

func JSON(cfg Config) (string, error) {
	if cfg.Listeners == nil {
		cfg.Listeners = []Listener{}
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	return string(append(data, '\n')), nil
}

func CacheKeys() []string {
	keys := append(store.PluginNames(), app.LiveSystemSummaryKey)
	sort.Strings(keys)
	return keys
}

func validateCacheKey(key string) error {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == app.LiveSystemSummaryKey || store.IsPluginName(key) {
		return nil
	}
	return fmt.Errorf("unknown cache_ttl key %q", key)
}
