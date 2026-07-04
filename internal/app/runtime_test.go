package app

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	healthpkg "github.com/mordilloSan/go-monitoring/internal/health"
)

func TestStartContextCreatesDatabaseAndServesAPI(t *testing.T) {
	tmpDir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a, err := New(ctx, tmpDir)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- a.StartContext(ctx, RunOptions{
			Listeners:         []ListenerOptions{{Name: "metrics", Address: "127.0.0.1:0", APIs: []string{"metrics"}}},
			CollectorInterval: 5 * time.Minute,
		})
	}()

	require.Eventually(t, func() bool {
		return testListenAddr(a) != ""
	}, 20*time.Second, 50*time.Millisecond)
	listenAddr := testListenAddr(a)

	_, err = os.Stat(tmpDir + "/metrics.db")
	require.NoError(t, err)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + listenAddr + "/api/v1/all")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), `"cpu":`)

	old := time.Now().Add(-2 * time.Minute)
	require.NoError(t, os.Chtimes(healthpkg.FilePath(), old, old))

	resp, err = client.Get("http://" + listenAddr + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Contains(t, string(body), `"healthy":false`)

	cancel()
	require.NoError(t, <-errCh)
}

func TestStartContextUnixSocket(t *testing.T) {
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "agent.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a, err := New(ctx, tmpDir)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- a.StartContext(ctx, RunOptions{
			Listeners:         []ListenerOptions{{Name: "metrics", Address: "unix:" + socketPath, APIs: []string{"metrics"}}},
			CollectorInterval: 5 * time.Minute,
		})
	}()

	require.Eventually(t, func() bool {
		return testListenAddr(a) != ""
	}, 20*time.Second, 50*time.Millisecond)
	assert.Equal(t, socketPath, testListenAddr(a))

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", socketPath)
			},
		},
	}
	resp, err := client.Get("http://unix/api/v1/meta")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), `"version":`)

	cancel()
	require.NoError(t, <-errCh)
	// The unix listener unlinks its socket file on shutdown.
	assert.NoFileExists(t, socketPath)
}

func TestStartContextDisabledHTTP(t *testing.T) {
	tmpDir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a, err := New(ctx, tmpDir)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- a.StartContext(ctx, RunOptions{
			CollectorInterval: 5 * time.Minute,
		})
	}()

	// The collector still persists snapshots without the HTTP server. The
	// health file is touched after the first snapshot is written, so waiting
	// for it means the initial collection is done and the event loop is up.
	require.Eventually(t, func() bool {
		_, statErr := os.Stat(healthpkg.FilePath())
		return statErr == nil
	}, 20*time.Second, 50*time.Millisecond)
	_, err = os.Stat(tmpDir + "/metrics.db")
	require.NoError(t, err)
	assert.Empty(t, a.Listeners())

	cancel()
	require.NoError(t, <-errCh)
}

func testListenAddr(a *App) string {
	listeners := a.Listeners()
	if len(listeners) == 0 {
		return ""
	}
	return listeners[0].EffectiveAddress
}
