package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mordilloSan/go-monitoring/internal/app"
	"github.com/mordilloSan/go-monitoring/internal/store"
)

const EnvConfigFile = "CONFIG_FILE"

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
	Listen            string              `json:"listen"`
	CollectorInterval Duration            `json:"collector_interval"`
	History           string              `json:"history"`
	CacheTTL          map[string]Duration `json:"cache_ttl"`
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
		Listen:            ":45876",
		CollectorInterval: Duration(app.DefaultCollectorInterval),
		History:           strings.Join(store.DefaultHistoryPluginNames(), ","),
		CacheTTL:          cacheTTL,
	}
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
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return false, err
	}
	return true, nil
}

func Validate(cfg Config) error {
	if cfg.CollectorInterval.Duration() <= 0 {
		return fmt.Errorf("collector_interval must be greater than zero")
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
	if value, ok := env("LISTEN"); ok && strings.TrimSpace(value) != "" {
		cfg.Listen = value
	} else if value, ok := env("PORT"); ok && strings.TrimSpace(value) != "" {
		cfg.Listen = value
	}
	if value, ok := env("HISTORY"); ok {
		cfg.History = value
	}

	ttls := ToDurationMap(cfg.CacheTTL)
	app.ApplyLiveCurrentTTLEnv(ttls, env)
	cfg.CacheTTL = FromDurationMap(ttls)
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
