package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mordilloSan/go-monitoring/internal/app"
	"github.com/mordilloSan/go-monitoring/internal/config"
	"github.com/mordilloSan/go-monitoring/internal/health"
	buildinfo "github.com/mordilloSan/go-monitoring/internal/version"
)

// runMainMenu drives the top-level interactive menu shown when the CLI is
// started with no arguments on a terminal (or with the menu command).
func runMainMenu(ctx context.Context, opts cmdOptions) int {
	if !shouldRunConfigMenu() {
		fmt.Fprintln(os.Stderr, "The interactive menu requires a terminal; run \"go-monitoring help\" for commands.")
		return 1
	}
	menu := &configMenu{
		in:   os.Stdin,
		out:  os.Stdout,
		hint: "Use Up/Down, Enter to select, q or Esc to quit.",
	}
	items := []string{
		"Agent",
		"Config",
		"Database",
		"API",
		"Quit",
	}
	cursor := 0
	for {
		choice, err := menu.selectItem("go-monitoring "+buildinfo.Version, "Config: "+opts.configPath, items, &cursor)
		if err != nil {
			return configCommandErrorCode(err)
		}
		if code, done := handleMainMenuChoice(ctx, menu, opts, choice); done {
			return code
		}
	}
}

func handleMainMenuChoice(ctx context.Context, menu *configMenu, opts cmdOptions, choice int) (code int, done bool) {
	switch choice {
	case 0:
		code, exit, err := menu.agentMenu(ctx, opts)
		if err != nil {
			return configCommandErrorCode(err), true
		}
		return code, exit
	case 1:
		if code, exit := menuEditConfig(ctx, menu, opts); exit {
			return code, true
		}
	case 2:
		if err := menu.databaseMenu(ctx, opts); err != nil {
			return configCommandErrorCode(err), true
		}
	case 3:
		if err := menu.apiMenu(ctx, opts); err != nil {
			return configCommandErrorCode(err), true
		}
	default:
		return 0, true
	}
	return 0, false
}

// selectItem runs an arrow-key selection over items and returns the chosen
// index, or -1 when the user backs out with q or Esc. The terminal is back in
// cooked mode by the time it returns.
func (m *configMenu) selectItem(title, subtitle string, items []string, cursor *int) (int, error) {
	return m.selectMenuItem(title, subtitle, menuItemsFromLabels(items), cursor)
}

func (m *configMenu) selectMenuItem(title, subtitle string, items []menuItem, cursor *int) (int, error) {
	if err := m.enterRaw(); err != nil {
		return -1, err
	}
	defer m.exitRaw()
	if !normalizeMenuCursor(items, cursor) {
		return -1, nil
	}
	for {
		m.renderMenu(title, subtitle, true, items, *cursor)
		key, err := m.readKey()
		if err != nil {
			return -1, err
		}
		switch key {
		case menuKeyUp:
			*cursor = nextEnabledMenuIndex(items, *cursor, -1)
		case menuKeyDown:
			*cursor = nextEnabledMenuIndex(items, *cursor, 1)
		case menuKeyQuit, menuKeyEscape:
			return -1, nil
		case menuKeyInterrupt:
			return -1, m.interrupt()
		case menuKeyEnter:
			if !items[*cursor].disabled {
				return *cursor, nil
			}
		}
	}
}

func normalizeMenuCursor(items []menuItem, cursor *int) bool {
	if len(items) == 0 {
		*cursor = 0
		return false
	}
	if *cursor >= 0 && *cursor < len(items) && !items[*cursor].disabled {
		return true
	}
	for i, item := range items {
		if !item.disabled {
			*cursor = i
			return true
		}
	}
	return false
}

func nextEnabledMenuIndex(items []menuItem, cursor, step int) int {
	if len(items) == 0 {
		return 0
	}
	next := cursor
	for range items {
		next = (next + step + len(items)) % len(items)
		if !items[next].disabled {
			return next
		}
	}
	return cursor
}

// pause keeps an action's output on screen until the user is done reading it;
// the next render clears the screen.
func (m *configMenu) pause(ctx context.Context) error {
	fmt.Fprint(m.out, "\nPress Enter to return to the menu.")
	done := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(m.in)
		_, err := reader.ReadString('\n')
		done <- err
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		fmt.Fprintln(m.out)
		return errConfigMenuInterrupted
	}
}

func printMenuStatusError(out io.Writer, err error) {
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
		fmt.Fprintln(out, "Agent API is not reachable.")
		fmt.Fprintln(out, "Start the agent from this menu, or run: sudo systemctl start go-monitoring.service")
		fmt.Fprintln(out, "Detail:", err)
		return
	}
	fmt.Fprintln(out, "Status check failed:", err)
}

func (m *configMenu) agentMenu(ctx context.Context, opts cmdOptions) (code int, exit bool, err error) {
	cursor := 0
	for {
		apiReachable := agentAPIReachable(ctx, opts)
		items := buildAgentItems(apiReachable, apiReachable || localAgentProcessExists(opts) || detachedAgentFilesExist())
		choice, err := m.selectMenuItem("Agent", "Run or inspect the agent", items, &cursor)
		if err != nil {
			return 0, true, err
		}
		switch choice {
		case 0:
			return menuRunAgent(ctx, opts), true, nil
		case 1:
			if err := m.startAgentBackground(ctx, opts); err != nil {
				return 0, true, err
			}
		case 2:
			if err := m.stopAgentBackground(ctx, opts); err != nil {
				return 0, true, err
			}
		case 3:
			if err := menuStatus(ctx, m, opts); err != nil {
				return 0, true, err
			}
		default:
			return 0, false, nil
		}
	}
}

func buildAgentItems(apiReachable, stoppable bool) []menuItem {
	start := menuItem{label: "Start agent (background)"}
	if apiReachable {
		start = menuItem{label: "Start agent (background, already running)", disabled: true}
	}
	stop := menuItem{label: "Stop/remove background agent"}
	if !stoppable {
		stop.disabled = true
	}
	status := menuItem{label: "Agent status"}
	if !apiReachable {
		status = menuItem{label: "Agent status (not running)", disabled: true}
	}
	return []menuItem{
		{label: "Run agent (foreground)"},
		start,
		stop,
		status,
		{label: "Back"},
	}
}

func agentAPIReachable(ctx context.Context, opts cmdOptions) bool {
	target, enabled, err := menuAgentTarget(opts)
	if err != nil || !enabled {
		return false
	}
	checkCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	return probeAgentHealth(checkCtx, target) == nil
}

func menuAgentTarget(opts cmdOptions) (agentTarget, bool, error) {
	effective, err := loadEffectiveConfig(opts)
	if err != nil {
		return agentTarget{}, false, err
	}
	addr := app.GetAddress(effective.cfg.Listen)
	if app.IsListenDisabled(addr) {
		return agentTarget{}, false, nil
	}
	target, err := statusTarget(effective.cfg.Listen)
	return target, true, err
}

func probeAgentHealth(ctx context.Context, target agentTarget) error {
	var health healthResponse
	_, err := getJSON(ctx, target.client, target.baseURL+"/healthz", &health)
	return err
}

func waitForAgentAPI(ctx context.Context, opts cmdOptions, timeout time.Duration) (reachable bool, enabled bool, err error) {
	target, enabled, err := menuAgentTarget(opts)
	if err != nil || !enabled {
		return false, enabled, err
	}

	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		checkCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		lastErr = probeAgentHealth(checkCtx, target)
		cancel()
		if lastErr == nil {
			return true, true, nil
		}
		if time.Now().After(deadline) {
			return false, true, lastErr
		}
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			return false, true, ctx.Err()
		}
	}
}

type systemdStartResult int

const (
	systemdStarted systemdStartResult = iota
	systemdUnavailable
	systemdFailed
)

func (m *configMenu) startAgentBackground(ctx context.Context, opts cmdOptions) error {
	m.clearScreen()
	if agentAPIReachable(ctx, opts) {
		fmt.Fprintln(m.out, "Agent API is already reachable.")
		return m.pause(ctx)
	}

	result, detail, err := startSystemdService(ctx)
	switch result {
	case systemdStarted:
		fmt.Fprintln(m.out, "Started go-monitoring.service.")
		m.printAgentAPIStartResult(ctx, opts, "")
		return m.pause(ctx)
	case systemdFailed:
		fmt.Fprintln(m.out, "Service start failed:", err)
		if detail != "" {
			fmt.Fprintln(m.out, detail)
		}
		return m.pause(ctx)
	case systemdUnavailable:
		fmt.Fprintln(m.out, "No installed systemd unit found; starting a local background agent.")
	}

	return m.startDetachedAgentProcess(ctx, opts)
}

func startSystemdService(ctx context.Context) (systemdStartResult, string, error) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return systemdUnavailable, "systemctl not found", err
	}

	cmd := exec.CommandContext(ctx, "systemctl", "start", "go-monitoring.service")
	output, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if systemdUnitUnavailable(detail) {
			return systemdUnavailable, detail, err
		}
		return systemdFailed, detail, err
	}
	return systemdStarted, strings.TrimSpace(string(output)), nil
}

func stopSystemdService(ctx context.Context) (systemdStartResult, string, error) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return systemdUnavailable, "systemctl not found", err
	}

	cmd := exec.CommandContext(ctx, "systemctl", "stop", "go-monitoring.service")
	output, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if systemdUnitUnavailable(detail) {
			return systemdUnavailable, detail, err
		}
		return systemdFailed, detail, err
	}
	return systemdStarted, strings.TrimSpace(string(output)), nil
}

func systemdUnitUnavailable(detail string) bool {
	return strings.Contains(detail, "Unit go-monitoring.service not found")
}

func (m *configMenu) startDetachedAgentProcess(ctx context.Context, opts cmdOptions) (retErr error) {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(m.out, "Could not resolve current executable:", err)
		return m.pause(ctx)
	}
	logPath, err := detachedAgentLogPath()
	if err != nil {
		fmt.Fprintln(m.out, "Could not prepare detached agent log:", err)
		return m.pause(ctx)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintln(m.out, "Could not open detached agent log:", err)
		return m.pause(ctx)
	}
	defer func() {
		if cerr := logFile.Close(); cerr != nil {
			if retErr == nil {
				retErr = fmt.Errorf("closing detached agent log file: %w", cerr)
			} else {
				fmt.Fprintln(m.out, "Warning: could not close detached agent log file:", cerr)
			}
		}
	}()
	pidPath, err := detachedAgentPIDPath(true)
	if err != nil {
		fmt.Fprintln(m.out, "Could not prepare detached agent pid file:", err)
		return m.pause(ctx)
	}

	args := detachedRunArgs(opts)
	cmd := exec.Command(exe, args...)
	cmd.Env = os.Environ()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(m.out, "Background start failed:", err)
		return m.pause(ctx)
	}
	pid := cmd.Process.Pid
	if err := writeDetachedAgentPID(pidPath, pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Process.Release()
		fmt.Fprintln(m.out, "Background start failed: could not write pid file:", err)
		return m.pause(ctx)
	}
	if err := cmd.Process.Release(); err != nil {
		fmt.Fprintln(m.out, "Background agent started, but process release failed:", err)
		return m.pause(ctx)
	}

	fmt.Fprintf(m.out, "Started local background agent (pid %d).\n", pid)
	m.printAgentAPIStartResult(ctx, opts, logPath)
	return m.pause(ctx)
}

func (m *configMenu) stopAgentBackground(ctx context.Context, opts cmdOptions) error {
	m.clearScreen()
	if pid, found := findLocalAgentPID(opts); found {
		return m.stopLocalAgent(ctx, pid)
	}

	result, detail, err := stopSystemdService(ctx)
	switch result {
	case systemdStarted:
		fmt.Fprintln(m.out, "Stopped go-monitoring.service.")
		return m.pause(ctx)
	case systemdFailed:
		fmt.Fprintln(m.out, "Service stop failed:", err)
		if detail != "" {
			fmt.Fprintln(m.out, detail)
		}
		return m.pause(ctx)
	default:
		if m.cleanupDetachedAgentFiles() {
			fmt.Fprintln(m.out, "No running menu-started background agent was found.")
			fmt.Fprintln(m.out, "Cleaned up local background agent files.")
			fmt.Fprintln(m.out, "Config and metrics database were left untouched.")
			return m.pause(ctx)
		}
		fmt.Fprintln(m.out, "No menu-started background agent was found.")
		fmt.Fprintln(m.out, "Config and metrics database were left untouched.")
		return m.pause(ctx)
	}
}

func (m *configMenu) stopLocalAgent(ctx context.Context, pid int) error {
	if err := terminateProcess(pid); err != nil {
		fmt.Fprintln(m.out, "Could not stop local background agent:", err)
		return m.pause(ctx)
	}

	fmt.Fprintf(m.out, "Stopped local background agent (pid %d).\n", pid)
	m.cleanupDetachedAgentFiles()
	fmt.Fprintln(m.out, "Config and metrics database were left untouched.")
	return m.pause(ctx)
}

func (m *configMenu) cleanupDetachedAgentFiles() bool {
	removed := make([]string, 0, 3)
	if pidPath, err := detachedAgentPIDPath(false); err == nil {
		removed = appendRemovedFile(removed, pidPath)
	}
	if logPath, err := detachedAgentLogPathFor(false); err == nil {
		removed = appendRemovedFile(removed, logPath)
	}
	removed = appendRemovedFile(removed, health.FilePath())
	if len(removed) == 0 {
		return false
	}
	fmt.Fprintln(m.out, "Removed:")
	for _, path := range removed {
		fmt.Fprintln(m.out, " ", path)
	}
	return true
}

func appendRemovedFile(removed []string, path string) []string {
	if err := os.Remove(path); err == nil {
		return append(removed, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return append(removed, path+" (remove failed: "+err.Error()+")")
	}
	return removed
}

func (m *configMenu) printAgentAPIStartResult(ctx context.Context, opts cmdOptions, logPath string) {
	reachable, enabled, err := waitForAgentAPI(ctx, opts, 3*time.Second)
	switch {
	case reachable:
		fmt.Fprintln(m.out, "Agent API is reachable.")
	case !enabled:
		fmt.Fprintln(m.out, "HTTP API is disabled in config; background agent status cannot be checked from the menu.")
	default:
		fmt.Fprintln(m.out, "Agent API did not respond yet.")
		if err != nil {
			fmt.Fprintln(m.out, "Detail:", err)
		}
	}
	if logPath != "" {
		fmt.Fprintln(m.out, "Log:", logPath)
	}
}

func detachedRunArgs(opts cmdOptions) []string {
	args := []string{"run", "--config", opts.configPath}
	if opts.listenSet {
		args = append(args, "--listen", opts.listen)
	}
	if opts.collectorIntervalSet {
		args = append(args, "--collector-interval", opts.collectorInterval.String())
	}
	if opts.historySet {
		args = append(args, "--history", opts.history)
	}
	if opts.apiCacheDefaultSet {
		args = append(args, "--api-cache-default", opts.apiCacheDefault.String())
	}
	if opts.apiCacheExpensiveSet {
		args = append(args, "--api-cache-expensive", opts.apiCacheExpensive.String())
	}
	keys := make([]string, 0, len(opts.cacheTTL.values))
	for key := range opts.cacheTTL.values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--api-cache", key+"="+opts.cacheTTL.values[key].String())
	}
	return args
}

func detachedAgentLogPath() (string, error) {
	return detachedAgentLogPathFor(true)
}

func detachedAgentLogPathFor(create bool) (string, error) {
	dir := "/var/log/go-monitoring"
	if os.Geteuid() != 0 {
		cacheDir, err := os.UserCacheDir()
		if err != nil {
			cacheDir = os.TempDir()
		}
		dir = filepath.Join(cacheDir, "go-monitoring")
	}
	if create {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	return filepath.Join(dir, "agent.log"), nil
}

func detachedAgentFilesExist() bool {
	for _, pathFunc := range []func() (string, error){
		func() (string, error) { return detachedAgentPIDPath(false) },
		func() (string, error) { return detachedAgentLogPathFor(false) },
	} {
		path, err := pathFunc()
		if err != nil {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

func detachedAgentPIDPath(create bool) (string, error) {
	dir := "/run/go-monitoring"
	if os.Geteuid() != 0 {
		if runtimeDir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); runtimeDir != "" {
			dir = filepath.Join(runtimeDir, "go-monitoring")
		} else {
			cacheDir, err := os.UserCacheDir()
			if err != nil {
				cacheDir = os.TempDir()
			}
			dir = filepath.Join(cacheDir, "go-monitoring")
		}
	}
	if create {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	return filepath.Join(dir, "local-agent.pid"), nil
}

func writeDetachedAgentPID(path string, pid int) error {
	return os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o644)
}

func localAgentProcessExists(opts cmdOptions) bool {
	_, found := findLocalAgentPID(opts)
	return found
}

func findLocalAgentPID(opts cmdOptions) (int, bool) {
	if pidPath, err := detachedAgentPIDPath(false); err == nil {
		pid, err := readDetachedAgentPID(pidPath)
		if err == nil && processRunning(pid) {
			return pid, true
		}
	}
	return findLocalAgentPIDFromProc(opts)
}

func readDetachedAgentPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func findLocalAgentPIDFromProc(opts cmdOptions) (int, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false
	}
	exe, _ := os.Executable()
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := splitProcCmdline(data)
		if localAgentArgsMatch(args, exe, opts.configPath) {
			return pid, true
		}
	}
	return 0, false
}

func splitProcCmdline(data []byte) []string {
	raw := strings.TrimRight(string(data), "\x00")
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\x00")
}

func localAgentArgsMatch(args []string, exe, configPath string) bool {
	if len(args) < 2 || !sameExecutable(args[0], exe) || !hasArg(args, "run") {
		return false
	}
	return hasConfigArg(args, configPath)
}

func sameExecutable(candidate, exe string) bool {
	if candidate == exe {
		return true
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return false
	}
	resolvedExe, err := filepath.EvalSymlinks(exe)
	return err == nil && resolved == resolvedExe
}

func hasArg(args []string, value string) bool {
	return slices.Contains(args, value)
}

func hasConfigArg(args []string, configPath string) bool {
	cleanConfigPath := filepath.Clean(configPath)
	for i, arg := range args {
		if arg == "--config" && i+1 < len(args) && filepath.Clean(args[i+1]) == cleanConfigPath {
			return true
		}
		if strings.HasPrefix(arg, "--config=") && filepath.Clean(strings.TrimPrefix(arg, "--config=")) == cleanConfigPath {
			return true
		}
	}
	return false
}

func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func terminateProcess(pid int) error {
	if !processRunning(pid) {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	deadline := time.Now().Add(3 * time.Second)
	for processRunning(pid) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if !processRunning(pid) {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func menuStatus(ctx context.Context, m *configMenu, opts cmdOptions) error {
	effective, err := loadEffectiveConfig(opts)
	if err != nil {
		fmt.Fprintln(m.out, "Invalid config:", err)
	} else if err := printStatus(ctx, effective.cfg); err != nil {
		printMenuStatusError(m.out, err)
	}
	return m.pause(ctx)
}

func (m *configMenu) apiMenu(ctx context.Context, opts cmdOptions) error {
	effective, err := loadEffectiveConfig(opts)
	if err != nil {
		fmt.Fprintln(m.out, "Invalid config:", err)
		return m.pause(ctx)
	}

	cfg := effective.cfg
	cursor := 0
	for {
		items := []string{
			"HTTP API Config (" + apiListenSummary(cfg.Listen) + ")",
			"Live API cache TTLs",
			"Reset API config",
			"Save API config",
			"Back",
		}
		choice, err := m.selectItem("API Config", "Config: "+opts.configPath, items, &cursor)
		if err != nil {
			return err
		}
		switch choice {
		case 0:
			if err := m.withRaw(func() error { return m.listenMenu(&cfg) }); err != nil {
				return err
			}
		case 1:
			if err := m.withRaw(func() error { return m.cacheMenu(&cfg) }); err != nil {
				return err
			}
		case 2:
			resetAPIConfig(&cfg)
		case 3:
			saved, err := m.save(opts.configPath, cfg)
			if err != nil {
				return err
			}
			if saved {
				return m.pause(ctx)
			}
		default:
			return nil
		}
	}
}

func resetAPIConfig(cfg *config.Config) {
	defaults := config.Default()
	cfg.Listen = defaults.Listen
	cfg.CacheTTL = defaults.CacheTTL
}

// menuEditConfig opens the existing config menu. exit is true when the whole
// process should end with code: after "Save and run" or an interrupt.
func menuEditConfig(ctx context.Context, m *configMenu, opts cmdOptions) (code int, exit bool) {
	effective, err := loadEffectiveConfig(opts)
	if err != nil {
		fmt.Fprintln(m.out, "Invalid config:", err)
		if pauseErr := m.pause(ctx); pauseErr != nil {
			return configCommandErrorCode(pauseErr), true
		}
		return 0, false
	}
	result, err := runConfigSectionMenu(opts.configPath, effective.cfg, effective.loaded)
	if err != nil {
		return configCommandErrorCode(err), true
	}
	if result.run {
		return startAgentExit(ctx, opts, result.cfg, "loaded"), true
	}
	if !result.pause {
		return 0, false
	}
	if err := m.pause(ctx); err != nil {
		return configCommandErrorCode(err), true
	}
	return 0, false
}

//nolint:gocognit // The branches map directly to interactive database menu choices.
func (m *configMenu) databaseMenu(ctx context.Context, opts cmdOptions) error {
	items := []string{
		"Check integrity",
		"Run maintenance",
		"Repair",
		"Reset (delete all history)",
		"Show database path",
		"Back",
	}
	actions := []string{"check", "maintain", "repair", "reset", "path"}
	cursor := 0
	for {
		choice, err := m.selectItem("Database", "Metrics database operations", items, &cursor)
		if err != nil {
			return err
		}
		if choice < 0 || choice >= len(actions) {
			return nil
		}

		dbOpts := cmdOptions{command: commandDB, dataDir: opts.dataDir, dbAction: actions[choice]}
		if dbOpts.dbAction == "reset" {
			confirmed, err := m.confirmReset()
			if err != nil {
				return err
			}
			if !confirmed {
				fmt.Fprintln(m.out, "Reset cancelled.")
				if err := m.pause(ctx); err != nil {
					return err
				}
				continue
			}
			dbOpts.dbForce = true
		}
		if err := handleDBCommand(dbOpts); err != nil {
			fmt.Fprintln(m.out, "Database command failed:", err)
		}
		if err := m.pause(ctx); err != nil {
			return err
		}
	}
}

func (m *configMenu) confirmReset() (bool, error) {
	value, changed, err := m.promptLine("Type yes to delete all stored metrics", "no")
	// promptLine re-arms raw mode for the config menu loops; database actions
	// print in cooked mode.
	m.exitRaw()
	if err != nil {
		return false, err
	}
	return changed && strings.EqualFold(value, "yes"), nil
}

func menuRunAgent(ctx context.Context, opts cmdOptions) int {
	effective, err := loadEffectiveConfig(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Invalid config:", err)
		return 1
	}
	configSource := effective.source
	if !effective.loaded {
		configSource = maybeSaveDefaultConfig(opts.configPath, effective.cfg, configSource)
	}
	return startAgentExit(ctx, opts, effective.cfg, configSource)
}
