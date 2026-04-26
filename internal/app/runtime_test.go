package app

import (
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	healthpkg "github.com/mordilloSan/go-monitoring/internal/health"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartContextCreatesDatabaseAndServesAPI(t *testing.T) {
	tmpDir := t.TempDir()

	a, err := New(tmpDir)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- a.StartContext(ctx, RunOptions{
			Addr:              "127.0.0.1:0",
			CollectorInterval: 5 * time.Minute,
		})
	}()

	require.Eventually(t, func() bool {
		return a.ListenAddr() != ""
	}, 20*time.Second, 50*time.Millisecond)

	_, err = os.Stat(tmpDir + "/metrics.db")
	require.NoError(t, err)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + a.ListenAddr() + "/api/v1/all")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), `"cpu":`)

	old := time.Now().Add(-2 * time.Minute)
	require.NoError(t, os.Chtimes(healthpkg.FilePath(), old, old))

	resp, err = client.Get("http://" + a.ListenAddr() + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Contains(t, string(body), `"healthy":false`)

	cancel()
	require.NoError(t, <-errCh)
}
