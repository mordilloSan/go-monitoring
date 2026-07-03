package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/mordilloSan/go-monitoring/internal/app"
	"github.com/mordilloSan/go-monitoring/internal/config"
	"github.com/mordilloSan/go-monitoring/internal/health"
	"github.com/mordilloSan/go-monitoring/internal/logging"
	"github.com/mordilloSan/go-monitoring/internal/store"
	"github.com/mordilloSan/go-monitoring/internal/utils"
	buildinfo "github.com/mordilloSan/go-monitoring/internal/version"
)

type command string

const (
	commandRun    command = "run"
	commandConfig command = "config"
	commandDB     command = "db"
	commandStatus command = "status"
	commandMenu   command = "menu"
)

type cmdOptions struct {
	command              command
	configPath           string
	listen               string
	listenSet            bool
	collectorInterval    time.Duration
	collectorIntervalSet bool
	history              string
	historySet           bool
	apiCacheDefault      time.Duration
	apiCacheDefaultSet   bool
	apiCacheExpensive    time.Duration
	apiCacheExpensiveSet bool
	cacheTTL             durationMapFlag
	configPrint          bool
	configInit           bool
	configAction         string
	dataDir              string
	dbForce              bool
	dbAction             string
}

// parse fills opts from argv (argv[0] is the program name). It returns
// done=true when the command was fully handled, along with the exit code.
//
//nolint:gocognit // CLI parsing keeps legacy aliases, command dispatch, and flag wiring together.
func (opts *cmdOptions) parse(argv []string) (bool, int) {
	name := argv[0]
	args := argv[1:]
	if len(args) == 0 {
		// On a terminal, no arguments opens the interactive menu; scripts and
		// pipes keep getting the usage text.
		if shouldRunConfigMenu() {
			opts.command = commandMenu
			opts.configPath = config.DefaultPath()
			opts.cacheTTL = durationMapFlag{values: make(map[string]time.Duration)}
			return false, 0
		}
		printUsage(name)
		return true, 0
	}

	subcommand, commandFound := parseCommand(args[0])
	if commandFound {
		args = args[1:]
	} else if !strings.HasPrefix(args[0], "-") {
		fmt.Fprintf(os.Stderr, "Unknown command %q\n\n", args[0])
		printUsage(name)
		return true, 1
	}

	if subcommand == "health" {
		if err := health.Check(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return true, 1
		}
		fmt.Print("ok")
		return true, 0
	}

	fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	opts.command = command(subcommand)
	opts.cacheTTL = durationMapFlag{values: make(map[string]time.Duration)}

	fs.StringVar(&opts.configPath, "config", config.DefaultPath(), "Config file path")
	version := fs.BoolP("version", "v", false, "Show version information")
	help := fs.BoolP("help", "h", false, "Show this help message")

	if opts.command == commandRun || opts.command == commandConfig || opts.command == commandStatus || opts.command == commandMenu {
		fs.StringVarP(&opts.listen, "listen", "l", "", "Address, port, unix:/path socket, or none to disable the HTTP API")
		fs.DurationVar(&opts.collectorInterval, "collector-interval", 0, "Collector interval, for example 30s or 1m")
		fs.StringVar(&opts.history, "history", "", "Comma-separated history plugin allowlist, or all/none")
		fs.DurationVar(&opts.apiCacheDefault, "api-cache-default", 0, "Set every live API cache TTL")
		fs.DurationVar(&opts.apiCacheExpensive, "api-cache-expensive", 0, "Set expensive live API cache TTLs")
		fs.Var(&opts.cacheTTL, "api-cache", "Set a live API cache TTL as plugin=duration, repeatable")
	}
	if opts.command == commandConfig {
		fs.BoolVar(&opts.configPrint, "print", false, "Print the effective config")
		fs.BoolVar(&opts.configInit, "init", false, "Write the current defaults if the config file is absent")
	}
	if opts.command == commandDB || opts.command == commandMenu {
		fs.StringVar(&opts.dataDir, "data-dir", "", "Data directory containing metrics.db")
	}
	if opts.command == commandDB {
		fs.BoolVar(&opts.dbForce, "force", false, "Confirm destructive database actions")
	}
	fs.Usage = func() { printUsage(name) }

	args = normalizeLegacyArgs(args)
	if err := fs.Parse(args); err != nil {
		return true, 2
	}
	opts.listenSet = flagChanged(fs, "listen")
	opts.collectorIntervalSet = flagChanged(fs, "collector-interval")
	opts.historySet = flagChanged(fs, "history")
	opts.apiCacheDefaultSet = flagChanged(fs, "api-cache-default")
	opts.apiCacheExpensiveSet = flagChanged(fs, "api-cache-expensive")
	if opts.command == commandConfig && fs.NArg() > 0 {
		opts.configAction = strings.ToLower(fs.Arg(0))
	}
	if opts.command == commandDB && fs.NArg() > 0 {
		opts.dbAction = strings.ToLower(fs.Arg(0))
	}

	switch {
	case *version:
		fmt.Println(buildinfo.RepoName+"-agent", buildinfo.Version)
		return true, 0
	case *help || subcommand == "help":
		fs.Usage()
		return true, 0
	}

	return false, 0
}

func parseCommand(raw string) (string, bool) {
	normalized := strings.ToLower(strings.TrimLeft(raw, "-"))
	switch normalized {
	case "run", "config", "db", "health", "help", "status", "menu":
		return normalized, true
	default:
		return "help", false
	}
}

func normalizeLegacyArgs(args []string) []string {
	out := append([]string(nil), args...)
	for i, arg := range out {
		switch {
		case arg == "-listen":
			out[i] = "--listen"
		case strings.HasPrefix(arg, "-listen="):
			out[i] = "--listen" + arg[len("-listen"):]
		}
	}
	return out
}

func flagChanged(fs *pflag.FlagSet, name string) bool {
	flag := fs.Lookup(name)
	return flag != nil && flag.Changed
}

func printUsage(name string) {
	builder := strings.Builder{}
	builder.WriteString("Usage: ")
	builder.WriteString(name)
	builder.WriteString(" <command> [flags]\n\n")
	builder.WriteString("Commands:\n")
	builder.WriteString("  run          Start the monitoring agent\n")
	builder.WriteString("  menu         Interactive menu: run, status, config, and database operations\n")
	builder.WriteString("               (also the default when started on a terminal with no arguments)\n")
	builder.WriteString("  config       Open the config menu, print, initialize, or update the config file\n")
	builder.WriteString("  db           Check, maintain, repair, reset, or print the metrics database path\n")
	builder.WriteString("  health       Check if the latest persisted collection tick is fresh\n")
	builder.WriteString("  status       Query a running local agent\n")
	builder.WriteString("  help         Show this help message\n\n")
	builder.WriteString("Aliases:\n")
	builder.WriteString("  -run and -config are accepted for compatibility with dash-prefixed commands.\n\n")
	builder.WriteString("Common flags:\n")
	builder.WriteString("  --config path                 Config file path\n")
	builder.WriteString("  -h, --help                    Show this help message\n")
	builder.WriteString("  -v, --version                 Show version information\n\n")
	builder.WriteString("Run/config flags:\n")
	builder.WriteString("  -l, --listen address          Address, port, unix:/path socket, or none to disable the HTTP API\n")
	builder.WriteString("  --collector-interval duration Collector interval, for example 30s or 1m\n")
	builder.WriteString("  --history list                History plugin allowlist, all, or none\n")
	builder.WriteString("  --api-cache-default duration  Set every live API cache TTL\n")
	builder.WriteString("  --api-cache-expensive duration Set expensive live API cache TTLs\n")
	builder.WriteString("  --api-cache plugin=duration   Set one live API cache TTL, repeatable\n")
	builder.WriteString("\nConfig-only flags:\n")
	builder.WriteString("  --init                        Write defaults if the config file is absent\n")
	builder.WriteString("  --print                       Print the effective config\n")
	builder.WriteString("\nConfig actions:\n")
	builder.WriteString("  config path                   Print the config path\n")
	builder.WriteString("  config validate               Validate effective config\n")
	builder.WriteString("\nDB flags:\n")
	builder.WriteString("  --data-dir path               Data directory containing metrics.db\n")
	builder.WriteString("  --force                       Confirm db reset\n")
	builder.WriteString("\nDB actions:\n")
	builder.WriteString("  db check                      Verify database integrity and schema\n")
	builder.WriteString("  db maintain                   Run rollups, retention, vacuum, and integrity check\n")
	builder.WriteString("  db repair                     Move aside corrupt DB files and recreate if needed\n")
	builder.WriteString("  db reset --force              Move aside DB files and recreate an empty database\n")
	builder.WriteString("  db path                       Print the metrics database path\n")
	fmt.Print(builder.String())
}

type durationMapFlag struct {
	values map[string]time.Duration
}

func (f *durationMapFlag) String() string {
	if f == nil || len(f.values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(f.values))
	for key, value := range f.values {
		parts = append(parts, key+"="+value.String())
	}
	return strings.Join(parts, ",")
}

func (f *durationMapFlag) Set(raw string) error {
	key, value, ok := strings.Cut(raw, "=")
	if !ok {
		return fmt.Errorf("expected plugin=duration")
	}
	ttl, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return err
	}
	if ttl < 0 {
		return fmt.Errorf("duration must not be negative")
	}
	if f.values == nil {
		f.values = make(map[string]time.Duration)
	}
	f.values[strings.ToLower(strings.TrimSpace(key))] = ttl
	return nil
}

func (f *durationMapFlag) Type() string {
	return "plugin=duration"
}

func applyCLIToConfig(cfg *config.Config, opts cmdOptions) error {
	if opts.listenSet {
		cfg.Listen = opts.listen
	}
	if opts.collectorIntervalSet {
		if opts.collectorInterval <= 0 {
			return fmt.Errorf("collector interval must be greater than zero")
		}
		cfg.CollectorInterval = config.Duration(opts.collectorInterval)
	}
	if opts.historySet {
		cfg.History = opts.history
	}
	if opts.apiCacheDefaultSet {
		config.ApplyCacheDefault(cfg, opts.apiCacheDefault)
	}
	if opts.apiCacheExpensiveSet {
		config.ApplyCacheExpensive(cfg, opts.apiCacheExpensive)
	}
	for key, ttl := range opts.cacheTTL.values {
		if err := config.SetCacheTTL(cfg, key, ttl); err != nil {
			return err
		}
	}
	return config.Validate(*cfg)
}

func configWasMutated(opts cmdOptions) bool {
	return opts.listenSet ||
		opts.collectorIntervalSet ||
		opts.historySet ||
		opts.apiCacheDefaultSet ||
		opts.apiCacheExpensiveSet ||
		len(opts.cacheTTL.values) > 0
}

type effectiveConfig struct {
	cfg    config.Config
	loaded bool
	source string
}

func loadEffectiveConfig(opts cmdOptions) (effectiveConfig, error) {
	cfg, loaded, err := config.Load(opts.configPath)
	if err != nil {
		return effectiveConfig{}, err
	}
	source := "defaults"
	if loaded {
		source = "loaded"
	}
	config.ApplyEnv(&cfg, utils.GetEnv)
	if err := applyCLIToConfig(&cfg, opts); err != nil {
		return effectiveConfig{}, err
	}
	return effectiveConfig{cfg: cfg, loaded: loaded, source: source}, nil
}

func maybeSaveDefaultConfig(path string, cfg config.Config, source string) string {
	created, err := config.SaveIfMissing(path, cfg)
	if err != nil {
		slog.Warn("failed to create default config; using effective in-memory config", "path", path, "error", err)
		return source
	}
	if created {
		slog.Info("created default config", "path", path)
		return "created"
	}
	return source
}

func handleConfigCommand(opts cmdOptions, cfg config.Config, loaded bool, configSource string) (bool, config.Config, string, error) {
	switch opts.configAction {
	case "validate":
		fmt.Printf("Config valid: %s (source=%s)\n", opts.configPath, configSource)
		return false, cfg, configSource, nil
	case "":
	default:
		return false, cfg, configSource, fmt.Errorf("unknown config action %q", opts.configAction)
	}

	mutated := configWasMutated(opts)
	switch {
	case opts.configInit && loaded && !mutated:
		fmt.Println("Config already exists:", opts.configPath)
	case opts.configInit || mutated:
		if err := config.Save(opts.configPath, cfg); err != nil {
			return false, cfg, configSource, fmt.Errorf("failed to save config: %w", err)
		}
		fmt.Println("Saved config:", opts.configPath)
	}

	switch {
	case opts.configPrint:
		rendered, err := config.JSON(cfg)
		if err != nil {
			return false, cfg, configSource, fmt.Errorf("failed to render config: %w", err)
		}
		fmt.Print(rendered)
	case !opts.configInit && !mutated && shouldRunConfigMenu():
		result, err := runConfigMenu(opts.configPath, cfg, loaded)
		if err != nil {
			return false, cfg, configSource, fmt.Errorf("config menu failed: %w", err)
		}
		if result.run {
			return true, result.cfg, "loaded", nil
		}
	case !opts.configInit && !mutated:
		rendered, err := config.JSON(cfg)
		if err != nil {
			return false, cfg, configSource, fmt.Errorf("failed to render config: %w", err)
		}
		fmt.Print(rendered)
	}
	return false, cfg, configSource, nil
}

func startAgent(ctx context.Context, opts cmdOptions, cfg config.Config, configSource string) error {
	slog.Info("config ready",
		"path", opts.configPath,
		"source", configSource,
		"version", cfg.Version,
		"collector_interval", cfg.CollectorInterval.Duration(),
		"history", cfg.History,
		"cache_ttl_count", len(cfg.CacheTTL),
	)

	a, err := app.New(ctx)
	if err != nil {
		return fmt.Errorf("failed to create agent: %w", err)
	}
	a.SetLiveCurrentTTLs(config.ToDurationMap(cfg.CacheTTL))

	if err := a.StartContext(ctx, app.RunOptions{
		Addr:              app.GetAddress(cfg.Listen),
		CollectorInterval: cfg.CollectorInterval.Duration(),
		History:           cfg.History,
		HistorySet:        true,
		ConfigPath:        opts.configPath,
		ConfigSource:      configSource,
		ConfigVersion:     cfg.Version,
		CacheTTL:          config.ToDurationMap(cfg.CacheTTL),
		ReloadConfig: func() (app.ReloadOptions, error) {
			reloaded, err := loadEffectiveConfig(opts)
			if err != nil {
				return app.ReloadOptions{}, err
			}
			return app.ReloadOptions{
				CollectorInterval: reloaded.cfg.CollectorInterval.Duration(),
				History:           reloaded.cfg.History,
				HistorySet:        true,
				CacheTTL:          config.ToDurationMap(reloaded.cfg.CacheTTL),
				ConfigSource:      reloaded.source,
				ConfigVersion:     reloaded.cfg.Version,
			}, nil
		},
	}); err != nil {
		return fmt.Errorf("failed to start standalone agent: %w", err)
	}
	return nil
}

func resolveDataDir(path string) (string, error) {
	if strings.TrimSpace(path) != "" {
		return store.GetDataDir(path)
	}
	return store.GetDataDir()
}

func runDatabaseMaintenance(s *store.Store) error {
	if err := s.RunMaintenance(time.Now().UTC()); err != nil {
		return err
	}
	if err := s.Vacuum(); err != nil {
		return err
	}
	return s.IntegrityCheck()
}

func printMovedFiles(moved []string) {
	if len(moved) == 0 {
		return
	}
	fmt.Println("Moved old database files:")
	for _, path := range moved {
		fmt.Println(" ", path)
	}
}

func handleDBCommand(opts cmdOptions) error {
	action := opts.dbAction
	if action == "" {
		action = "check"
	}

	dataDir, err := resolveDataDir(opts.dataDir)
	if err != nil {
		return err
	}
	dbPath := store.DatabasePath(dataDir)

	switch action {
	case "path":
		fmt.Println(dbPath)
		return nil
	case "check":
		return handleDBCheck(dataDir, dbPath)
	case "maintain":
		return handleDBMaintain(dataDir)
	case "repair":
		return handleDBRepair(dataDir)
	case "reset":
		return handleDBReset(dataDir, opts.dbForce)
	default:
		return fmt.Errorf("unknown db action %q", opts.dbAction)
	}
}

func handleDBCheck(dataDir, dbPath string) error {
	if err := store.CheckDatabase(dataDir); err != nil {
		return err
	}
	fmt.Println("Database OK:", dbPath)
	return nil
}

func handleDBMaintain(dataDir string) error {
	s, err := store.OpenStore(dataDir)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := runDatabaseMaintenance(s); err != nil {
		return err
	}
	fmt.Println("Database maintained:", s.Path())
	return nil
}

func handleDBRepair(dataDir string) error {
	s, moved, err := store.RepairDatabase(dataDir)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := runDatabaseMaintenance(s); err != nil {
		return err
	}
	if len(moved) > 0 {
		fmt.Println("Database repaired:", s.Path())
		printMovedFiles(moved)
		return nil
	}
	fmt.Println("Database OK:", s.Path())
	return nil
}

func handleDBReset(dataDir string, force bool) error {
	if !force {
		return fmt.Errorf("db reset requires --force")
	}
	s, moved, err := store.ResetDatabase(dataDir)
	if err != nil {
		return err
	}
	defer s.Close()
	if err := s.IntegrityCheck(); err != nil {
		return err
	}
	fmt.Println("Database reset:", s.Path())
	printMovedFiles(moved)
	return nil
}

// Run executes the go-monitoring CLI and returns the process exit code.
// os.Exit must only be called by main so deferred cleanup always runs.
func Run(ctx context.Context, argv []string) int {
	var opts cmdOptions
	if done, code := opts.parse(argv); done {
		return code
	}
	logging.Configure("go-monitoring")
	if opts.command == commandMenu {
		return runMainMenu(ctx, opts)
	}
	if opts.command == commandConfig && opts.configAction == "path" {
		fmt.Println(opts.configPath)
		return 0
	}
	if opts.command == commandDB {
		if err := handleDBCommand(opts); err != nil {
			fmt.Fprintln(os.Stderr, "Database command failed:", err)
			return 1
		}
		return 0
	}

	effective, err := loadEffectiveConfig(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Invalid config:", err)
		return 1
	}
	cfg := effective.cfg
	configSource := effective.source

	if opts.command == commandRun && !effective.loaded {
		configSource = maybeSaveDefaultConfig(opts.configPath, cfg, effective.source)
	}

	if opts.command == commandConfig {
		run, updatedCfg, updatedSource, cmdErr := handleConfigCommand(opts, cfg, effective.loaded, configSource)
		if cmdErr != nil {
			return configCommandErrorCode(cmdErr)
		}
		if !run {
			return 0
		}
		cfg = updatedCfg
		configSource = updatedSource
	}

	if opts.command == commandStatus {
		if err := printStatus(ctx, cfg); err != nil {
			fmt.Fprintln(os.Stderr, "Status check failed:", err)
			return 1
		}
		return 0
	}

	return startAgentExit(ctx, opts, cfg, configSource)
}

// startAgentExit runs the agent until it stops and maps the result to a
// process exit code.
func startAgentExit(ctx context.Context, opts cmdOptions, cfg config.Config, configSource string) int {
	if err := startAgent(ctx, opts, cfg, configSource); err != nil {
		slog.Error("agent failed", "error", err)
		return 1
	}
	return 0
}

func configCommandErrorCode(err error) int {
	if errors.Is(err, errConfigMenuInterrupted) {
		return 130
	}
	fmt.Fprintln(os.Stderr, err)
	return 1
}
