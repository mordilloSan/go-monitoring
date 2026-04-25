//go:build !linux

package agent

import (
	"context"

	"github.com/mordilloSan/go-monitoring/internal/model/systemd"
)

// systemdManager manages the collection of systemd service statistics.
type systemdManager struct {
	hasFreshStats bool
}

// newSystemdManager creates a new systemdManager.
func newSystemdManager() (*systemdManager, error) {
	return &systemdManager{}, nil
}

func (sm *systemdManager) Start(_ context.Context) {}

func (sm *systemdManager) Stop() {}

func (sm *systemdManager) context() context.Context {
	return context.Background()
}

// getServiceStats returns nil for non-linux systems.
func (sm *systemdManager) getServiceStats(_ context.Context, _ any, _ bool) []*systemd.Service {
	return nil
}

// getFailedServiceCount returns 0 for non-linux systems.
func (sm *systemdManager) getFailedServiceCount() uint16 {
	return 0
}
