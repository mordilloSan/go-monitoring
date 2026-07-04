package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/mordilloSan/go-monitoring/internal/app"
	"github.com/mordilloSan/go-monitoring/internal/config"
	"github.com/mordilloSan/go-monitoring/internal/store"
)

type menuKey int

const (
	menuKeyUnknown menuKey = iota
	menuKeyUp
	menuKeyDown
	menuKeyEnter
	menuKeyEscape
	menuKeyQuit
	menuKeySave
	menuKeyInterrupt
)

const (
	ansiHideCursor = "\x1b[?25l"
	ansiShowCursor = "\x1b[?25h"
	ansiDim        = "\x1b[2m"
	ansiReset      = "\x1b[0m"
)

const (
	defaultTCPListen        = "127.0.0.1:45876"
	defaultAllTCPListen     = ":45876"
	defaultUnixSocketPath   = "/run/go-monitoring/agent.sock"
	defaultUnixSocketListen = "unix:" + defaultUnixSocketPath
)

// configMenu doubles as a small raw-terminal menu engine; the top-level
// interactive menu in main_menu.go reuses it.
type configMenu struct {
	in       *os.File
	out      io.Writer
	hint     string // footer hint; empty means the config-menu default
	rawState *term.State
}

type menuItem struct {
	label    string
	disabled bool
}

type configMenuResult struct {
	run   bool
	pause bool
	cfg   config.Config
}

var errConfigMenuInterrupted = errors.New("interrupted")

type configMenuOptions struct {
	exitLabel      string
	printNoChanges bool
}

func defaultConfigMenuOptions() configMenuOptions {
	return configMenuOptions{
		exitLabel:      "Exit without saving",
		printNoChanges: true,
	}
}

func shouldRunConfigMenu() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

func runConfigMenu(path string, cfg config.Config, loaded bool) (configMenuResult, error) {
	return runConfigMenuWithOptions(path, cfg, loaded, defaultConfigMenuOptions())
}

func runConfigSectionMenu(path string, cfg config.Config, loaded bool) (configMenuResult, error) {
	return runConfigMenuWithOptions(path, cfg, loaded, configMenuOptions{exitLabel: "Back"})
}

func runConfigMenuWithOptions(path string, cfg config.Config, loaded bool, opts configMenuOptions) (configMenuResult, error) {
	menu := &configMenu{
		in:  os.Stdin,
		out: os.Stdout,
	}
	return menu.runWithOptions(path, cfg, loaded, opts)
}

func (m *configMenu) runWithOptions(path string, cfg config.Config, loaded bool, opts configMenuOptions) (configMenuResult, error) {
	if err := m.enterRaw(); err != nil {
		return configMenuResult{}, err
	}
	defer m.exitRaw()

	cursor := 0
	for {
		items := mainMenuItems(cfg, opts.exitLabel)
		m.render("go-monitoring config", path, loaded, items, cursor)

		key, err := m.readKey()
		if err != nil {
			return configMenuResult{}, err
		}
		switch key {
		case menuKeyUp:
			cursor = (cursor + len(items) - 1) % len(items)
		case menuKeyDown:
			cursor = (cursor + 1) % len(items)
		case menuKeyQuit, menuKeyEscape:
			return m.leaveConfigMenu(opts), nil
		case menuKeyInterrupt:
			return configMenuResult{}, m.interrupt()
		case menuKeySave:
			if saved, saveErr := m.save(path, cfg); saveErr != nil || saved {
				return configMenuResult{pause: saved, cfg: cfg}, saveErr
			}
		case menuKeyEnter:
			result, done, err := m.handleRunEnter(cursor, &cfg, path, opts)
			if done || err != nil {
				return result, err
			}
		}
	}
}

func mainMenuItems(cfg config.Config, exitLabel string) []string {
	return []string{
		fmt.Sprintf("Collector interval: %s", cfg.CollectorInterval.Duration()),
		fmt.Sprintf("SMART refresh interval: %s", cfg.SmartRefreshInterval.Duration()),
		fmt.Sprintf("History plugins: %s", cfg.History),
		"Reset general settings",
		"Save and exit",
		"Save and run",
		exitLabel,
	}
}

func (m *configMenu) handleRunEnter(cursor int, cfg *config.Config, path string, opts configMenuOptions) (configMenuResult, bool, error) {
	switch cursor {
	case 0:
		next, changed, err := m.promptDuration("Collector interval", cfg.CollectorInterval.Duration(), validateCollectorInterval)
		if err != nil {
			return configMenuResult{}, true, err
		}
		if changed {
			cfg.CollectorInterval = config.Duration(next)
		}
	case 1:
		next, changed, err := m.promptDuration("SMART refresh interval", cfg.SmartRefreshInterval.Duration(), validateCollectorInterval)
		if err != nil {
			return configMenuResult{}, true, err
		}
		if changed {
			cfg.SmartRefreshInterval = config.Duration(next)
		}
	case 2:
		if err := m.historyMenu(cfg); err != nil {
			return configMenuResult{}, true, err
		}
	case 3:
		resetGeneralConfig(cfg)
	case 4:
		if saved, err := m.save(path, *cfg); err != nil || saved {
			return configMenuResult{pause: saved, cfg: *cfg}, true, err
		}
	case 5:
		if saved, err := m.save(path, *cfg); err != nil || saved {
			return configMenuResult{run: saved, cfg: *cfg}, true, err
		}
		_ = m.enterRaw()
	case 6:
		return m.leaveConfigMenu(opts), true, nil
	}
	return configMenuResult{}, false, nil
}

func (m *configMenu) leaveConfigMenu(opts configMenuOptions) configMenuResult {
	m.exitRaw()
	if opts.printNoChanges {
		fmt.Fprintln(m.out, "No changes saved.")
	}
	return configMenuResult{}
}

func resetGeneralConfig(cfg *config.Config) {
	defaults := config.Default()
	cfg.CollectorInterval = defaults.CollectorInterval
	cfg.SmartRefreshInterval = defaults.SmartRefreshInterval
	cfg.History = defaults.History
}

func (m *configMenu) listenMenu(cfg *config.Config) error {
	cursor := 0
	for {
		items := buildListenItems(*cfg)
		m.render("HTTP API Config", apiListenSummaryForConfig(*cfg), true, items, cursor)

		key, err := m.readKey()
		if err != nil {
			return err
		}
		switch key {
		case menuKeyUp:
			cursor = (cursor + len(items) - 1) % len(items)
		case menuKeyDown:
			cursor = (cursor + 1) % len(items)
		case menuKeyEscape, menuKeyQuit:
			return nil
		case menuKeyInterrupt:
			return m.interrupt()
		case menuKeyEnter:
			done, err := m.handleListenEnter(cursor, cfg, len(items))
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		}
	}
}

func buildListenItems(cfg config.Config) []string {
	tcpEnabled := metricsEnabled(cfg)
	unixEnabled := commandsEnabled(cfg)
	return []string{
		fmt.Sprintf("HTTP listener: %s", yesNo(tcpEnabled)),
		"HTTP bind: localhost (127.0.0.1:45876)",
		"HTTP bind: all interfaces (:45876)",
		fmt.Sprintf("HTTP address/port: %s", tcpListenPromptDefault(metricsListenValue(cfg))),
		fmt.Sprintf("Unix socket: %s", yesNo(unixEnabled)),
		fmt.Sprintf("Unix socket path: %s", unixSocketPromptDefault(commandListenValue(cfg))),
		"Disable API",
		"Back",
	}
}

func (m *configMenu) handleListenEnter(cursor int, cfg *config.Config, itemCount int) (bool, error) {
	if cursor == itemCount-1 {
		return true, nil
	}
	switch cursor {
	case 0:
		if metricsEnabled(*cfg) {
			removeListenerAPI(cfg, config.APIKindMetrics)
		} else {
			config.SetMetricsListener(cfg, defaultTCPListen)
		}
	case 1:
		config.SetMetricsListener(cfg, defaultTCPListen)
	case 2:
		config.SetMetricsListener(cfg, defaultAllTCPListen)
	case 3:
		next, changed, err := m.promptString("TCP listen address or port", tcpListenPromptDefault(metricsListenValue(*cfg)), validateTCPListen)
		if err != nil {
			return false, err
		}
		if changed {
			config.SetMetricsListener(cfg, next)
		}
	case 4:
		if commandsEnabled(*cfg) {
			removeListenerAPI(cfg, config.APIKindCommands)
		} else {
			setCommandListener(cfg, defaultUnixSocketListen)
		}
	case 5:
		next, changed, err := m.promptString("Unix socket path", unixSocketPromptDefault(commandListenValue(*cfg)), validateUnixSocketPath)
		if err != nil {
			return false, err
		}
		if changed {
			setCommandListener(cfg, unixSocketListenValue(next))
		}
	case 6:
		cfg.Listeners = nil
	}
	return false, nil
}

func apiListenSummaryForConfig(cfg config.Config) string {
	return "unix: " + yesNo(commandsEnabled(cfg)) + "; HTTP: " + yesNo(metricsEnabled(cfg))
}

func metricsEnabled(cfg config.Config) bool {
	_, ok := config.MetricsListener(cfg)
	return ok
}

func commandsEnabled(cfg config.Config) bool {
	_, ok := config.CommandListener(cfg)
	return ok
}

func metricsListenValue(cfg config.Config) string {
	if listener, ok := config.MetricsListener(cfg); ok {
		return listener.Address
	}
	return app.ListenDisabled
}

func commandListenValue(cfg config.Config) string {
	if listener, ok := config.CommandListener(cfg); ok {
		return listener.Address
	}
	return app.ListenDisabled
}

func setCommandListener(cfg *config.Config, listen string) {
	normalized := app.GetAddress(listen)
	if app.IsListenDisabled(normalized) {
		removeListenerAPI(cfg, config.APIKindCommands)
		return
	}
	for i := range cfg.Listeners {
		if listenerHasAPI(cfg.Listeners[i], config.APIKindCommands) {
			cfg.Listeners[i].Address = normalized
			cfg.Listeners[i].APIs = ensureListenerAPI(cfg.Listeners[i].APIs, config.APIKindCommands)
			return
		}
	}
	cfg.Listeners = append(cfg.Listeners, config.Listener{Name: "control", Address: normalized, APIs: []string{config.APIKindCommands}})
}

func removeListenerAPI(cfg *config.Config, api string) {
	out := cfg.Listeners[:0]
	for _, listener := range cfg.Listeners {
		apis := removeAPI(listener.APIs, api)
		if len(apis) == 0 {
			continue
		}
		listener.APIs = apis
		out = append(out, listener)
	}
	cfg.Listeners = out
}

func removeAPI(apis []string, api string) []string {
	out := make([]string, 0, len(apis))
	for _, value := range apis {
		if strings.EqualFold(strings.TrimSpace(value), api) {
			continue
		}
		out = append(out, value)
	}
	return out
}

func ensureListenerAPI(apis []string, api string) []string {
	for _, value := range apis {
		if strings.EqualFold(strings.TrimSpace(value), api) {
			return apis
		}
	}
	return append(apis, api)
}

func listenerHasAPI(listener config.Listener, api string) bool {
	for _, value := range listener.APIs {
		if strings.EqualFold(strings.TrimSpace(value), api) {
			return true
		}
	}
	return false
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func (m *configMenu) historyMenu(cfg *config.Config) error {
	cursor := 0
	selected, err := selectedHistoryPlugins(cfg.History)
	if err != nil {
		return err
	}
	plugins := store.PluginNames()
	for {
		items := buildHistoryItems(plugins, selected)
		m.render("History plugins", historyFromSelection(selected), true, items, cursor)

		key, err := m.readKey()
		if err != nil {
			return err
		}
		switch key {
		case menuKeyUp:
			cursor = (cursor + len(items) - 1) % len(items)
		case menuKeyDown:
			cursor = (cursor + 1) % len(items)
		case menuKeyEscape, menuKeyQuit:
			cfg.History = historyFromSelection(selected)
			return nil
		case menuKeyInterrupt:
			return m.interrupt()
		case menuKeyEnter:
			done := applyHistoryEnter(cursor, plugins, selected, len(items))
			cfg.History = historyFromSelection(selected)
			if done {
				return nil
			}
		}
	}
}

func buildHistoryItems(plugins []string, selected map[string]bool) []string {
	items := make([]string, 0, len(plugins)+3)
	items = append(items, "Select all", "Select none")
	for _, plugin := range plugins {
		marker := "[ ]"
		if selected[plugin] {
			marker = "[x]"
		}
		items = append(items, marker+" "+plugin)
	}
	return append(items, "Back")
}

func applyHistoryEnter(cursor int, plugins []string, selected map[string]bool, itemCount int) bool {
	if cursor == itemCount-1 {
		return true
	}
	switch cursor {
	case 0:
		for _, p := range plugins {
			selected[p] = true
		}
	case 1:
		for _, p := range plugins {
			selected[p] = false
		}
	default:
		selected[plugins[cursor-2]] = !selected[plugins[cursor-2]]
	}
	return false
}

func (m *configMenu) cacheMenu(cfg *config.Config) error {
	cursor := 0
	keys := config.CacheKeys()
	for {
		items := buildCacheItems(keys, cfg)
		m.render("Live API cache TTLs", "0s disables a live API response cache", true, items, cursor)

		key, err := m.readKey()
		if err != nil {
			return err
		}
		switch key {
		case menuKeyUp:
			cursor = (cursor + len(items) - 1) % len(items)
		case menuKeyDown:
			cursor = (cursor + 1) % len(items)
		case menuKeyEscape, menuKeyQuit:
			return nil
		case menuKeyInterrupt:
			return m.interrupt()
		case menuKeyEnter:
			done, err := m.handleCacheEnter(cursor, keys, cfg, len(items))
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		}
	}
}

func buildCacheItems(keys []string, cfg *config.Config) []string {
	items := make([]string, 0, len(keys)+4)
	items = append(items, "Set all TTLs", "Set expensive TTLs", "Reset TTLs to defaults")
	for _, key := range keys {
		items = append(items, fmt.Sprintf("%s: %s", key, cfg.CacheTTL[key].Duration()))
	}
	return append(items, "Back")
}

func (m *configMenu) handleCacheEnter(cursor int, keys []string, cfg *config.Config, itemCount int) (bool, error) {
	if cursor == itemCount-1 {
		return true, nil
	}
	switch cursor {
	case 0:
		next, changed, err := m.promptDuration("TTL for all live API caches", 2*time.Second, validateCacheTTL)
		if err != nil {
			return false, err
		}
		if changed {
			config.ApplyCacheDefault(cfg, next)
		}
	case 1:
		next, changed, err := m.promptDuration("TTL for expensive live API caches", 10*time.Second, validateCacheTTL)
		if err != nil {
			return false, err
		}
		if changed {
			config.ApplyCacheExpensive(cfg, next)
		}
	case 2:
		cfg.CacheTTL = config.Default().CacheTTL
	default:
		if err := m.setCacheTTL(keys[cursor-3], cfg); err != nil {
			return false, err
		}
	}
	return false, nil
}

func (m *configMenu) setCacheTTL(key string, cfg *config.Config) error {
	next, changed, err := m.promptDuration("Cache TTL for "+key, cfg.CacheTTL[key].Duration(), validateCacheTTL)
	if err != nil {
		return err
	}
	if changed {
		return config.SetCacheTTL(cfg, key, next)
	}
	return nil
}

func (m *configMenu) render(title, subtitle string, loaded bool, items []string, cursor int) {
	m.renderMenu(title, subtitle, loaded, menuItemsFromLabels(items), cursor)
}

func (m *configMenu) clearScreen() {
	fmt.Fprint(m.out, "\x1b[H\x1b[2J")
}

func (m *configMenu) renderMenu(title, subtitle string, loaded bool, items []menuItem, cursor int) {
	var builder strings.Builder
	builder.WriteString("\x1b[H\x1b[2J")
	builder.WriteString(title)
	builder.WriteString("\n")
	builder.WriteString(strings.Repeat("=", len(title)))
	builder.WriteString("\n")
	if subtitle != "" {
		builder.WriteString(subtitle)
		builder.WriteString("\n")
	}
	if !loaded {
		builder.WriteString("No config file found; editing built-in defaults.")
		builder.WriteString("\n")
	}
	builder.WriteString("\n")
	hint := m.hint
	if hint == "" {
		hint = "Use Up/Down, Enter to edit/select, s to save, q or Esc to exit."
	}
	builder.WriteString(hint)
	builder.WriteString("\n\n")

	for i, item := range items {
		prefix := "  "
		if i == cursor {
			prefix = "> "
		}
		builder.WriteString(prefix)
		if item.disabled {
			builder.WriteString(ansiDim)
		}
		builder.WriteString(item.label)
		if item.disabled {
			builder.WriteString(ansiReset)
		}
		builder.WriteString("\n")
	}
	fmt.Fprint(m.out, strings.ReplaceAll(builder.String(), "\n", "\r\n"))
}

func menuItemsFromLabels(labels []string) []menuItem {
	items := make([]menuItem, 0, len(labels))
	for _, label := range labels {
		items = append(items, menuItem{label: label})
	}
	return items
}

func (m *configMenu) readKey() (menuKey, error) {
	var buf [3]byte
	n, err := m.in.Read(buf[:1])
	if err != nil || n == 0 {
		return menuKeyUnknown, err
	}
	switch buf[0] {
	case '\r', '\n':
		return menuKeyEnter, nil
	case 'q', 'Q':
		return menuKeyQuit, nil
	case 's', 'S':
		return menuKeySave, nil
	case 0x03:
		return menuKeyInterrupt, nil
	case 0x1b:
		n, err = m.in.Read(buf[1:2])
		if err != nil || n == 0 {
			return menuKeyEscape, err
		}
		if buf[1] != '[' {
			return menuKeyEscape, nil
		}
		n, err = m.in.Read(buf[2:3])
		if err != nil || n == 0 {
			return menuKeyEscape, err
		}
		switch buf[2] {
		case 'A':
			return menuKeyUp, nil
		case 'B':
			return menuKeyDown, nil
		}
	}
	return menuKeyUnknown, nil
}

func (m *configMenu) interrupt() error {
	m.exitRaw()
	fmt.Fprintln(m.out, "\nInterrupted.")
	return errConfigMenuInterrupted
}

func (m *configMenu) promptString(label, current string, validate func(string) error) (string, bool, error) {
	for {
		value, changed, err := m.promptLine(label, current)
		if err != nil || !changed {
			return current, false, err
		}
		if err := validate(value); err != nil {
			if waitErr := m.showPromptError(err); waitErr != nil {
				return current, false, waitErr
			}
			continue
		}
		return value, true, nil
	}
}

func (m *configMenu) promptDuration(label string, current time.Duration, validate func(time.Duration) error) (time.Duration, bool, error) {
	for {
		value, changed, err := m.promptLine(label, current.String())
		if err != nil || !changed {
			return current, false, err
		}
		parsed, err := time.ParseDuration(value)
		if err != nil {
			if waitErr := m.showPromptError(err); waitErr != nil {
				return current, false, waitErr
			}
			continue
		}
		if err := validate(parsed); err != nil {
			if waitErr := m.showPromptError(err); waitErr != nil {
				return current, false, waitErr
			}
			continue
		}
		return parsed, true, nil
	}
}

func (m *configMenu) promptLine(label, current string) (string, bool, error) {
	m.exitRaw()
	defer func() {
		_ = m.enterRaw()
	}()

	fmt.Fprintf(m.out, "\n%s [%s]: ", label, current)
	reader := bufio.NewReader(m.in)
	raw, err := reader.ReadString('\n')
	if err != nil {
		return "", false, err
	}
	value := strings.TrimSpace(raw)
	if value == "" {
		return current, false, nil
	}
	return value, true, nil
}

func (m *configMenu) showPromptError(err error) error {
	m.exitRaw()
	defer func() {
		_ = m.enterRaw()
	}()

	fmt.Fprintf(m.out, "Invalid value: %v\nPress Enter to try again.", err)
	reader := bufio.NewReader(m.in)
	_, readErr := reader.ReadString('\n')
	return readErr
}

func (m *configMenu) save(path string, cfg config.Config) (bool, error) {
	if err := config.Save(path, cfg); err != nil {
		if waitErr := m.showPromptError(err); waitErr != nil {
			return false, waitErr
		}
		return false, nil
	}
	m.exitRaw()
	fmt.Fprintln(m.out, "Saved config:", path)
	return true, nil
}

func (m *configMenu) withRaw(fn func() error) error {
	alreadyRaw := m.rawState != nil
	if !alreadyRaw {
		if err := m.enterRaw(); err != nil {
			return err
		}
		defer m.exitRaw()
	}
	return fn()
}

func (m *configMenu) enterRaw() error {
	if m.rawState != nil {
		return nil
	}
	state, err := term.MakeRaw(int(m.in.Fd()))
	if err != nil {
		return err
	}
	m.rawState = state
	m.hideCursor()
	return nil
}

func (m *configMenu) exitRaw() {
	if m.rawState == nil {
		return
	}
	_ = term.Restore(int(m.in.Fd()), m.rawState)
	m.rawState = nil
	m.showCursor()
}

func (m *configMenu) hideCursor() {
	if m.out != nil {
		fmt.Fprint(m.out, ansiHideCursor)
	}
}

func (m *configMenu) showCursor() {
	if m.out != nil {
		fmt.Fprint(m.out, ansiShowCursor)
	}
}

func validateListen(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("listen address cannot be empty")
	}
	return nil
}

func validateTCPListen(value string) error {
	if err := validateListen(value); err != nil {
		return err
	}
	normalized := app.GetAddress(value)
	if app.IsListenDisabled(normalized) {
		return fmt.Errorf("use Disable HTTP API to turn the API off")
	}
	if network, _ := app.SplitListenAddress(normalized); network != "tcp" {
		return fmt.Errorf("tcp listen value must be a port or host:port")
	}
	if _, _, err := net.SplitHostPort(normalized); err != nil {
		return fmt.Errorf("tcp listen value must be a port or host:port: %w", err)
	}
	return nil
}

func validateUnixSocketPath(value string) error {
	if err := validateListen(value); err != nil {
		return err
	}
	network, address := app.SplitListenAddress(strings.TrimSpace(value))
	if network != "unix" {
		return fmt.Errorf("unix socket path must be absolute or unix:/absolute/path")
	}
	if !strings.HasPrefix(address, "/") {
		return fmt.Errorf("unix socket path must be absolute")
	}
	return nil
}

func tcpListenPromptDefault(listen string) string {
	normalized := app.GetAddress(listen)
	if app.IsListenDisabled(normalized) {
		return defaultTCPListen
	}
	if network, _ := app.SplitListenAddress(normalized); network != "tcp" {
		return defaultTCPListen
	}
	return normalized
}

func unixSocketPromptDefault(listen string) string {
	normalized := app.GetAddress(listen)
	if network, address := app.SplitListenAddress(normalized); network == "unix" && address != "" {
		return address
	}
	return defaultUnixSocketPath
}

func unixSocketListenValue(value string) string {
	_, address := app.SplitListenAddress(strings.TrimSpace(value))
	return "unix:" + address
}

func validateCollectorInterval(d time.Duration) error {
	if d <= 0 {
		return fmt.Errorf("duration must be greater than zero")
	}
	return nil
}

func validateCacheTTL(value time.Duration) error {
	if value < 0 {
		return fmt.Errorf("duration must not be negative")
	}
	return nil
}

func selectedHistoryPlugins(raw string) (map[string]bool, error) {
	plugins, err := store.ParseHistoryPlugins(raw, true)
	if err != nil {
		return nil, err
	}
	selected := make(map[string]bool, len(store.PluginNames()))
	for _, plugin := range plugins {
		selected[plugin] = true
	}
	return selected, nil
}

func historyFromSelection(selected map[string]bool) string {
	plugins := store.PluginNames()
	out := make([]string, 0, len(plugins))
	for _, plugin := range plugins {
		if selected[plugin] {
			out = append(out, plugin)
		}
	}
	if len(out) == 0 {
		return "none"
	}
	if len(out) == len(plugins) {
		return "all"
	}
	return strings.Join(out, ",")
}
