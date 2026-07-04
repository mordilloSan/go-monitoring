package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mordilloSan/go-monitoring/internal/config"
)

func TestApplyConfigSetParamsUpdatesSmartRefreshInterval(t *testing.T) {
	cfg := config.Default()
	raw := "2h"

	restartRequired, err := applyConfigSetParams(&cfg, configSetParams{
		SmartRefreshInterval: &raw,
	})

	require.NoError(t, err)
	assert.False(t, restartRequired)
	assert.Equal(t, 2*time.Hour, cfg.SmartRefreshInterval.Duration())
}

func TestApplyConfigSetParamsRejectsInvalidSmartRefreshInterval(t *testing.T) {
	cfg := config.Default()
	raw := "0"

	_, err := applyConfigSetParams(&cfg, configSetParams{
		SmartRefreshInterval: &raw,
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "smart_refresh_interval")
}
