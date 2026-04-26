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
		items := []string{
			fmt.Sprintf("Listen address: %s", cfg.Listen),
			fmt.Sprintf("Collector interval: %s", cfg.CollectorInterval.Duration()),
			fmt.Sprintf("History plugins: %s", cfg.History),
			"Live API cache TTLs",
			"Reset to defaults",
			"Save and exit",
			"Save and run",
			"Exit without saving",
		}
		m.render("go-monitoring config", path, loaded, items, cursor)

		switch key, err := m.readKey(); {
		case err != nil:
			return configMenuResult{}, err
		case key == menuKeyUp:
			cursor = (cursor + len(items) - 1) % len(items)
		case key == menuKeyDown:
			cursor = (cursor + 1) % len(items)
		case key == menuKeyQuit || key == menuKeyEscape:
			m.exitRaw()
			fmt.Fprintln(m.out, "No changes saved.")
			return configMenuResult{}, nil
		case key == menuKeySave:
			if saved, saveErr := m.save(path, cfg); saveErr != nil || saved {
				return configMenuResult{cfg: cfg}, saveErr
			}
		case key == menuKeyEnter:
			switch cursor {
			case 0:
				next, changed, promptErr := m.promptString("Listen address", cfg.Listen, validateListen)
				if promptErr != nil {
					return configMenuResult{}, promptErr
				}
				if changed {
					cfg.Listen = next
				}
			case 1:
				next, changed, promptErr := m.promptDuration("Collector interval", cfg.CollectorInterval.Duration(), func(value time.Duration) error {
					if value <= 0 {
						return fmt.Errorf("duration must be greater than zero")
					}
					return nil
				})
				if promptErr != nil {
					return configMenuResult{}, promptErr
				}
				if changed {
					cfg.CollectorInterval = config.Duration(next)
				}
			case 2:
				if err := m.historyMenu(&cfg); err != nil {
					return configMenuResult{}, err
				}
			case 3:
				if err := m.cacheMenu(&cfg); err != nil {
					return configMenuResult{}, err
				}
			case 4:
				cfg = config.Default()
			case 5:
				if saved, saveErr := m.save(path, cfg); saveErr != nil || saved {
					return configMenuResult{cfg: cfg}, saveErr
				}
			case 6:
				if saved, saveErr := m.save(path, cfg); saveErr != nil || saved {
					return configMenuResult{run: saved, cfg: cfg}, saveErr
				}
				_ = m.enterRaw()
			case 7:
				m.exitRaw()
				fmt.Fprintln(m.out, "No changes saved.")
				return configMenuResult{}, nil
			}
		}
	}
}

func (m *configMenu) historyMenu(cfg *config.Config) error {
	cursor := 0
	selected, err := selectedHistoryPlugins(cfg.History)
	if err != nil {
		return err
	}
	plugins := store.PluginNames()
	for {
		items := make([]string, 0, len(plugins)+3)
		items = append(items, "Select all", "Select none")
		for _, plugin := range plugins {
			marker := "[ ]"
			if selected[plugin] {
				marker = "[x]"
			}
			items = append(items, marker+" "+plugin)
		}
		items = append(items, "Back")
		m.render("History plugins", historyFromSelection(selected), true, items, cursor)

		switch key, err := m.readKey(); {
		case err != nil:
			return err
		case key == menuKeyUp:
			cursor = (cursor + len(items) - 1) % len(items)
		case key == menuKeyDown:
			cursor = (cursor + 1) % len(items)
		case key == menuKeyEscape || key == menuKeyQuit:
			cfg.History = historyFromSelection(selected)
			return nil
		case key == menuKeyEnter:
			switch cursor {
			case 0:
				for _, plugin := range plugins {
					selected[plugin] = true
				}
			case 1:
				for _, plugin := range plugins {
					selected[plugin] = false
				}
			case len(items) - 1:
				cfg.History = historyFromSelection(selected)
				return nil
			default:
				plugin := plugins[cursor-2]
				selected[plugin] = !selected[plugin]
			}
			cfg.History = historyFromSelection(selected)
		}
	}
}

func (m *configMenu) cacheMenu(cfg *config.Config) error {
	cursor := 0
	keys := config.CacheKeys()
	for {
		items := make([]string, 0, len(keys)+4)
		items = append(items, "Set all TTLs", "Set expensive TTLs", "Reset TTLs to defaults")
		for _, key := range keys {
			items = append(items, fmt.Sprintf("%s: %s", key, cfg.CacheTTL[key].Duration()))
		}
		items = append(items, "Back")
		m.render("Live API cache TTLs", "0s disables a live API response cache", true, items, cursor)

		switch key, err := m.readKey(); {
		case err != nil:
			return err
		case key == menuKeyUp:
			cursor = (cursor + len(items) - 1) % len(items)
		case key == menuKeyDown:
			cursor = (cursor + 1) % len(items)
		case key == menuKeyEscape || key == menuKeyQuit:
			return nil
		case key == menuKeyEnter:
			if cursor == len(items)-1 {
				return nil
			}
			switch cursor {
			case 0:
				next, changed, promptErr := m.promptDuration("TTL for all live API caches", 2*time.Second, validateCacheTTL)
				if promptErr != nil {
					return promptErr
				}
				if changed {
					config.ApplyCacheDefault(cfg, next)
				}
			case 1:
				next, changed, promptErr := m.promptDuration("TTL for expensive live API caches", 10*time.Second, validateCacheTTL)
				if promptErr != nil {
					return promptErr
				}
				if changed {
					config.ApplyCacheExpensive(cfg, next)
				}
			case 2:
				cfg.CacheTTL = config.Default().CacheTTL
			default:
				cacheKey := keys[cursor-3]
				next, changed, promptErr := m.promptDuration("Cache TTL for "+cacheKey, cfg.CacheTTL[cacheKey].Duration(), validateCacheTTL)
				if promptErr != nil {
					return promptErr
				}
				if changed {
					if err := config.SetCacheTTL(cfg, cacheKey, next); err != nil {
						return err
					}
				}
			}
		}
	}
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

func validateHistory(value string) error {
	_, err := store.ParseHistoryPlugins(value, true)
	return err
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
