package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/mordilloSan/go-monitoring/internal/app"
	"github.com/mordilloSan/go-monitoring/internal/config"
	"github.com/mordilloSan/go-monitoring/internal/health"
	"github.com/mordilloSan/go-monitoring/internal/utils"
	buildinfo "github.com/mordilloSan/go-monitoring/internal/version"
	"github.com/spf13/pflag"
)

type command string

const (
	commandRun    command = "run"
	commandConfig command = "config"
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
}

func (opts *cmdOptions) parse() bool {
	args := os.Args[1:]
	if len(args) == 0 {
		printUsage(os.Args[0])
		return true
	}

	subcommand, commandFound := parseCommand(args[0])
	if commandFound {
		args = args[1:]
	} else if !strings.HasPrefix(args[0], "-") {
		fmt.Fprintf(os.Stderr, "Unknown command %q\n\n", args[0])
		printUsage(os.Args[0])
		return true
	}

	if subcommand == "health" {
		if err := health.Check(); err != nil {
			log.Fatal(err)
		}
		fmt.Print("ok")
		return true
	}

	fs := pflag.NewFlagSet(os.Args[0], pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	opts.command = command(subcommand)
	opts.cacheTTL = durationMapFlag{values: make(map[string]time.Duration)}

	fs.StringVar(&opts.configPath, "config", config.DefaultPath(), "Config file path")
	version := fs.BoolP("version", "v", false, "Show version information")
	help := fs.BoolP("help", "h", false, "Show this help message")

	if opts.command == commandRun || opts.command == commandConfig {
		fs.StringVarP(&opts.listen, "listen", "l", "", "Address or port to listen on")
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
	fs.Usage = func() { printUsage(os.Args[0]) }

	args = normalizeLegacyArgs(args)
	if err := fs.Parse(args); err != nil {
		return true
	}
	opts.listenSet = flagChanged(fs, "listen")
	opts.collectorIntervalSet = flagChanged(fs, "collector-interval")
	opts.historySet = flagChanged(fs, "history")
	opts.apiCacheDefaultSet = flagChanged(fs, "api-cache-default")
	opts.apiCacheExpensiveSet = flagChanged(fs, "api-cache-expensive")

	switch {
	case *version:
		fmt.Println(buildinfo.RepoName+"-agent", buildinfo.Version)
		return true
	case *help || subcommand == "help":
		fs.Usage()
		return true
	}

	return false
}

func parseCommand(raw string) (string, bool) {
	normalized := strings.ToLower(strings.TrimLeft(raw, "-"))
	switch normalized {
	case "run", "config", "health", "help":
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
	builder.WriteString("  config       Open the config menu, print, initialize, or update the config file\n")
	builder.WriteString("  health       Check if the latest persisted collection tick is fresh\n")
	builder.WriteString("  help         Show this help message\n\n")
	builder.WriteString("Aliases:\n")
	builder.WriteString("  -run and -config are accepted for compatibility with dash-prefixed commands.\n\n")
	builder.WriteString("Common flags:\n")
	builder.WriteString("  --config path                 Config file path\n")
	builder.WriteString("  -h, --help                    Show this help message\n")
	builder.WriteString("  -v, --version                 Show version information\n\n")
	builder.WriteString("Run/config flags:\n")
	builder.WriteString("  -l, --listen address          Address or port to listen on\n")
	builder.WriteString("  --collector-interval duration Collector interval, for example 30s or 1m\n")
	builder.WriteString("  --history list                History plugin allowlist, all, or none\n")
	builder.WriteString("  --api-cache-default duration  Set every live API cache TTL\n")
	builder.WriteString("  --api-cache-expensive duration Set expensive live API cache TTLs\n")
	builder.WriteString("  --api-cache plugin=duration   Set one live API cache TTL, repeatable\n")
	builder.WriteString("\nConfig-only flags:\n")
	builder.WriteString("  --init                        Write defaults if the config file is absent\n")
	builder.WriteString("  --print                       Print the effective config\n")
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

func main() {
	var opts cmdOptions
	if opts.parse() {
		return
	}

	cfg, loaded, err := config.Load(opts.configPath)
	if err != nil {
		log.Fatal("Failed to load config: ", err)
	}
	if opts.command == commandRun && !loaded {
		if created, err := config.SaveIfMissing(opts.configPath, cfg); err != nil {
			log.Printf("Failed to create default config at %s; using built-in defaults: %v", opts.configPath, err)
		} else if created {
			log.Printf("Created default config: %s", opts.configPath)
		}
	}
	config.ApplyEnv(&cfg, utils.GetEnv)
	err = applyCLIToConfig(&cfg, opts)
	if err != nil {
		log.Fatal("Invalid config: ", err)
	}

	if opts.command == commandConfig {
		switch {
		case opts.configInit && loaded && !configWasMutated(opts):
			fmt.Println("Config already exists:", opts.configPath)
		case opts.configInit || configWasMutated(opts):
			err = config.Save(opts.configPath, cfg)
			if err != nil {
				log.Fatal("Failed to save config: ", err)
			}
			fmt.Println("Saved config:", opts.configPath)
		}
		switch {
		case opts.configPrint:
			var rendered string
			rendered, err = config.JSON(cfg)
			if err != nil {
				log.Fatal("Failed to render config: ", err)
			}
			fmt.Print(rendered)
		case !opts.configInit && !configWasMutated(opts) && shouldRunConfigMenu():
			err = runConfigMenu(opts.configPath, cfg, loaded)
			if err != nil {
				log.Fatal("Config menu failed: ", err)
			}
		case !opts.configInit && !configWasMutated(opts):
			var rendered string
			rendered, err = config.JSON(cfg)
			if err != nil {
				log.Fatal("Failed to render config: ", err)
			}
			fmt.Print(rendered)
		}
		return
	}

	a, err := app.New()
	if err != nil {
		log.Fatal("Failed to create agent: ", err)
	}
	a.SetLiveCurrentTTLs(config.ToDurationMap(cfg.CacheTTL))

	if err := a.Start(app.RunOptions{
		Addr:              app.GetAddress(cfg.Listen),
		CollectorInterval: cfg.CollectorInterval.Duration(),
		History:           cfg.History,
		HistorySet:        true,
	}); err != nil {
		log.Fatal("Failed to start standalone agent: ", err)
	}
}
