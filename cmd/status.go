package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	apimodel "github.com/mordilloSan/go-monitoring/internal/api/model"
	"github.com/mordilloSan/go-monitoring/internal/app"
	"github.com/mordilloSan/go-monitoring/internal/config"
)

type healthResponse struct {
	Healthy     bool    `json:"healthy"`
	LastUpdated string  `json:"last_updated"`
	AgeSeconds  float64 `json:"age_seconds"`
}

func printStatus(ctx context.Context, cfg config.Config) error {
	target, err := statusTarget(cfg.Listen)
	if err != nil {
		return err
	}

	var health healthResponse
	healthCode, err := getJSON(ctx, target.client, target.baseURL+"/healthz", &health)
	if err != nil {
		return err
	}
	var meta apimodel.MetaResponse
	metaCode, err := getJSON(ctx, target.client, target.baseURL+"/api/v1/meta", &meta)
	if err != nil {
		return err
	}

	fmt.Printf("Agent: %s\n", target.display)
	fmt.Printf("Health: %t (status=%d age=%.1fs last_updated=%s)\n", health.Healthy, healthCode, health.AgeSeconds, health.LastUpdated)
	fmt.Printf("Version: %s\n", meta.Version)
	fmt.Printf("Collector interval: %s\n", meta.CollectorInterval)
	fmt.Printf("Listen address: %s\n", meta.ListenAddr)
	fmt.Printf("Data dir: %s\n", meta.DataDir)
	fmt.Printf("Database: %s\n", meta.DBPath)
	if meta.Config.Path != "" {
		fmt.Printf("Config: %s (source=%s version=%d)\n", meta.Config.Path, meta.Config.Source, meta.Config.Version)
		fmt.Printf("History plugins: %s\n", strings.Join(meta.Config.HistoryPlugins, ","))
	}
	if metaCode != http.StatusOK {
		return fmt.Errorf("meta returned HTTP %d", metaCode)
	}
	if healthCode != http.StatusOK {
		return fmt.Errorf("health returned HTTP %d", healthCode)
	}
	return nil
}

func getJSON(ctx context.Context, client *http.Client, url string, target any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return resp.StatusCode, err
	}
	return resp.StatusCode, nil
}

// agentTarget is how the status command reaches a running agent: an HTTP
// client wired for TCP or a unix socket, the base URL for requests, and the
// address to show the user.
type agentTarget struct {
	client  *http.Client
	baseURL string
	display string
}

func statusTarget(listen string) (agentTarget, error) {
	addr := app.GetAddress(listen)
	if app.IsListenDisabled(addr) {
		return agentTarget{}, fmt.Errorf("the HTTP API is disabled (listen=%s); use \"go-monitoring health\" for a local liveness check", addr)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	network, address := app.SplitListenAddress(addr)
	if network == "unix" {
		client.Transport = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", address)
			},
		}
		return agentTarget{client: client, baseURL: "http://unix", display: "unix:" + address}, nil
	}

	baseURL, err := statusBaseURL(address)
	if err != nil {
		return agentTarget{}, err
	}
	return agentTarget{client: client, baseURL: baseURL, display: baseURL}, nil
}

// statusBaseURL turns a TCP listen address into a dialable local base URL.
func statusBaseURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	// net.JoinHostPort brackets IPv6 hosts itself.
	return "http://" + net.JoinHostPort(host, port), nil
}
