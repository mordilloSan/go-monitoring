package agent

import (
	"testing"

	"github.com/mordilloSan/go-monitoring/internal/common"
	modelnet "github.com/mordilloSan/go-monitoring/internal/model/network"
	procmodel "github.com/mordilloSan/go-monitoring/internal/model/process"
	"github.com/mordilloSan/go-monitoring/internal/model/system"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatherStatsDoesNotAttachDetailsToCachedRequests(t *testing.T) {
	agent := &Agent{
		cache: NewSystemDataCache(),
		systemInfoManager: &systemInfoManager{
			systemDetails: system.Details{Hostname: "updated-host", Podman: true},
			detailsDirty:  true,
		},
	}
	cached := &system.CombinedData{
		Info: system.Info{Hostname: "cached-host"},
	}
	agent.cache.Set(cached, defaultDataCacheTimeMs)

	response := agent.gatherStats(common.DataRequestOptions{CacheTimeMs: defaultDataCacheTimeMs})

	assert.Same(t, cached, response)
	assert.Nil(t, response.Details)
	assert.True(t, agent.systemInfoManager.detailsDirty)
	assert.Equal(t, "cached-host", response.Info.Hostname)
	assert.Nil(t, cached.Details)

	secondResponse := agent.gatherStats(common.DataRequestOptions{CacheTimeMs: defaultDataCacheTimeMs})
	assert.Same(t, cached, secondResponse)
	assert.Nil(t, secondResponse.Details)
}

func TestUpdateSystemDetailsMarksDetailsDirty(t *testing.T) {
	m := &systemInfoManager{}

	m.updateSystemDetails(func(details *system.Details) {
		details.Hostname = "updated-host"
		details.Podman = true
	})

	assert.True(t, m.detailsDirty)
	assert.Equal(t, "updated-host", m.systemDetails.Hostname)
	assert.True(t, m.systemDetails.Podman)

	original := &system.CombinedData{}
	realTimeResponse := m.attachSystemDetails(original, 1000, true)
	assert.Same(t, original, realTimeResponse)
	assert.Nil(t, realTimeResponse.Details)
	assert.True(t, m.detailsDirty)

	response := m.attachSystemDetails(original, defaultDataCacheTimeMs, false)
	require.NotNil(t, response.Details)
	assert.NotSame(t, original, response)
	assert.Equal(t, "updated-host", response.Details.Hostname)
	assert.True(t, response.Details.Podman)
	assert.False(t, m.detailsDirty)
	assert.Nil(t, original.Details)
}

func TestCacheableStatsDataDropsCurrentOnlyPayloads(t *testing.T) {
	original := &system.CombinedData{
		ProcessCount: &procmodel.Count{Total: 1},
		Processes:    []procmodel.Process{{PID: 123}},
		Programs:     []procmodel.Program{{Name: "go-monitoring"}},
		Connections:  &modelnet.ConnectionStats{Total: 2},
		IRQs:         []modelnet.IRQStat{{IRQ: "1"}},
	}

	cached := cacheableStatsData(original)

	require.NotNil(t, cached)
	assert.NotSame(t, original, cached)
	assert.Nil(t, cached.ProcessCount)
	assert.Nil(t, cached.Processes)
	assert.Nil(t, cached.Programs)
	assert.Nil(t, cached.Connections)
	assert.Nil(t, cached.IRQs)
	assert.NotNil(t, original.ProcessCount)
	assert.NotNil(t, original.Processes)
	assert.NotNil(t, original.Programs)
	assert.NotNil(t, original.Connections)
	assert.NotNil(t, original.IRQs)
}
