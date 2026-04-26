package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mordilloSan/go-monitoring/internal/config"
	"github.com/mordilloSan/go-monitoring/internal/store"
	"golang.org/x/term"
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
)

type configMenu struct {
	in       *os.File
	out      io.Writer
	rawState *term.State
}

type configMenuResult struct {
	run bool
	cfg config.Config
}

func shouldRunConfigMenu() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

func runConfigMenu(path string, cfg config.Config, loaded bool) (configMenuResult, error) {
	menu := &configMenu{
		in:  os.Stdin,
		out: os.Stdout,
	}
	return menu.run(path, cfg, loaded)
}

func (m *configMenu) run(path string, cfg config.Config, loaded bool) (configMenuResult, error) {
	if err := m.enterRaw(); err != nil {
		return configMenuResult{}, err
	}
	defer m.exitRaw()

	cursor := 0
	for {
		items := mainMenuItems(cfg)
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
			m.exitRaw()
			fmt.Fprintln(m.out, "No changes saved.")
			return configMenuResult{}, nil
		case menuKeySave:
			if saved, saveErr := m.save(path, cfg); saveErr != nil || saved {
				return configMenuResult{cfg: cfg}, saveErr
			}
		case menuKeyEnter:
			result, done, err := m.handleRunEnter(cursor, &cfg, path)
			if done || err != nil {
				return result, err
			}
		}
	}
}

func mainMenuItems(cfg config.Config) []string {
	return []string{
		fmt.Sprintf("Listen address: %s", cfg.Listen),
		fmt.Sprintf("Collector interval: %s", cfg.CollectorInterval.Duration()),
		fmt.Sprintf("History plugins: %s", cfg.History),
		"Live API cache TTLs",
		"Reset to defaults",
		"Save and exit",
		"Save and run",
		"Exit without saving",
	}
}

func (m *configMenu) handleRunEnter(cursor int, cfg *config.Config, path string) (configMenuResult, bool, error) {
	switch cursor {
	case 0:
		next, changed, err := m.promptString("Listen address", cfg.Listen, validateListen)
		if err != nil {
			return configMenuResult{}, true, err
		}
		if changed {
			cfg.Listen = next
		}
	case 1:
		next, changed, err := m.promptDuration("Collector interval", cfg.CollectorInterval.Duration(), validateCollectorInterval)
		if err != nil {
			return configMenuResult{}, true, err
		}
		if changed {
			cfg.CollectorInterval = config.Duration(next)
		}
	case 2:
		if err := m.historyMenu(cfg); err != nil {
			return configMenuResult{}, true, err
		}
	case 3:
		if err := m.cacheMenu(cfg); err != nil {
			return configMenuResult{}, true, err
		}
	case 4:
		*cfg = config.Default()
	case 5:
		if saved, err := m.save(path, *cfg); err != nil || saved {
			return configMenuResult{cfg: *cfg}, true, err
		}
	case 6:
		if saved, err := m.save(path, *cfg); err != nil || saved {
			return configMenuResult{run: saved, cfg: *cfg}, true, err
		}
		_ = m.enterRaw()
	case 7:
		m.exitRaw()
		fmt.Fprintln(m.out, "No changes saved.")
		return configMenuResult{}, true, nil
	}
	return configMenuResult{}, false, nil
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
	builder.WriteString("Use Up/Down, Enter to edit/select, s to save, q or Esc to exit.")
	builder.WriteString("\n\n")

	for i, item := range items {
		prefix := "  "
		if i == cursor {
			prefix = "> "
		}
		builder.WriteString(prefix)
		builder.WriteString(item)
		builder.WriteString("\n")
	}
	fmt.Fprint(m.out, strings.ReplaceAll(builder.String(), "\n", "\r\n"))
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

func (m *configMenu) enterRaw() error {
	if m.rawState != nil {
		return nil
	}
	state, err := term.MakeRaw(int(m.in.Fd()))
	if err != nil {
		return err
	}
	m.rawState = state
	return nil
}

func (m *configMenu) exitRaw() {
	if m.rawState == nil {
		return
	}
	_ = term.Restore(int(m.in.Fd()), m.rawState)
	m.rawState = nil
}

func validateListen(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("listen address cannot be empty")
	}
	return nil
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
