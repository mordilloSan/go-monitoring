package health

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealth(t *testing.T) {
	// Override healthFile to use a temporary directory for this test.
	originalHealthFile := healthFile
	tmpDir := t.TempDir()
	healthFile = filepath.Join(tmpDir, "go_monitoring_health_test")
	defer func() { healthFile = originalHealthFile }()

	t.Run("check with no health file", func(t *testing.T) {
		err := Check()
		require.Error(t, err)
		assert.True(t, os.IsNotExist(err), "expected a file-not-exist error, but got: %v", err)
	})

	t.Run("update and check", func(t *testing.T) {
		err := Update()
		require.NoError(t, err, "Update() failed")

		err = Check()
		assert.NoError(t, err, "Check() failed immediately after Update()")
	})

	// This test uses synctest to simulate time passing.
	t.Run("check with simulated time", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			// Update the file to set the initial timestamp.
			require.NoError(t, Update(), "Update() failed inside synctest")

			// Set the mtime to the current fake time to align the file's timestamp with the simulated clock.
			now := time.Now()
			require.NoError(t, os.Chtimes(healthFile, now, now), "Chtimes failed")

			// Wait a duration less than the threshold.
			time.Sleep(89 * time.Second)
			synctest.Wait()

			// The check should still pass.
			assert.NoError(t, Check(), "Check() failed after 89s")

			// Wait for the total duration to exceed the threshold.
			time.Sleep(5 * time.Second)
			synctest.Wait()

			// The check should now fail as unhealthy.
			err := Check()
			require.Error(t, err, "Check() should have failed after 91s")
			assert.Equal(t, "unhealthy", err.Error(), "Check() returned wrong error")
		})
	})
}

func TestHealthFilePathDoesNotCreateHealthFile(t *testing.T) {
	preferredDir := t.TempDir()
	fallbackDir := t.TempDir()

	path := healthFilePath(preferredDir, fallbackDir)

	assert.Equal(t, filepath.Join(preferredDir, healthFilename), path)
	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err), "path selection should not refresh the health timestamp")
}

func TestHealthFilePathUsesFallbackWhenPreferredMissing(t *testing.T) {
	missingPreferredDir := filepath.Join(t.TempDir(), "missing")
	fallbackDir := t.TempDir()

	path := healthFilePath(missingPreferredDir, fallbackDir)

	assert.Equal(t, filepath.Join(fallbackDir, healthFilename), path)
}
