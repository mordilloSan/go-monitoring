package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveAutoCollectorPriority(t *testing.T) {
	gm := &GPUManager{}

	t.Run("prefers host native collectors in auto mode", func(t *testing.T) {
		got := gm.resolveAutoCollectorPriority(gpuCapabilities{
			hasNvidiaSmi:   true,
			hasAmdSysfs:    true,
			hasRocmSmi:     true,
			hasIntelGpuTop: true,
		})
		want := []collectorSource{
			collectorSourceNvidiaSMI,
			collectorSourceNVML,
			collectorSourceAmdSysfs,
			collectorSourceRocmSMI,
			collectorSourceIntelGpuTop,
		}
		assert.Equal(t, want, got)
	})

	t.Run("keeps nvtop as last resort", func(t *testing.T) {
		got := gm.resolveAutoCollectorPriority(gpuCapabilities{
			hasNvtop: true,
		})
		assert.Equal(t, []collectorSource{collectorSourceNVTop}, got)
	})

	t.Run("keeps apple collectors opt in", func(t *testing.T) {
		got := gm.resolveAutoCollectorPriority(gpuCapabilities{
			hasMacmon:       true,
			hasPowermetrics: true,
		})
		assert.Empty(t, got)
	})

	t.Run("skips nvidia collector order on jetson", func(t *testing.T) {
		got := gm.resolveAutoCollectorPriority(gpuCapabilities{
			hasNvidiaSmi:  true,
			hasTegrastats: true,
		})
		assert.Empty(t, got)
	})
}
