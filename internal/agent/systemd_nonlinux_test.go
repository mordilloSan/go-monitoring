//go:build !linux && testing

package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewSystemdManager(t *testing.T) {
	manager, err := newSystemdManager()
	assert.NoError(t, err)
	assert.NotNil(t, manager)
}

func TestSystemdManagerGetServiceStats(t *testing.T) {
	manager, err := newSystemdManager()
	assert.NoError(t, err)

	// Test with refresh = true
	result := manager.getServiceStats(context.Background(), "any-service", true)
	assert.Nil(t, result)

	// Test with refresh = false
	result = manager.getServiceStats(context.Background(), "any-service", false)
	assert.Nil(t, result)
}

func TestSystemdManagerFields(t *testing.T) {
	manager, err := newSystemdManager()
	assert.NoError(t, err)

	// The non-linux manager should be a simple struct with no special fields
	// We can't test private fields directly, but we can test the methods work
	assert.NotNil(t, manager)
}
