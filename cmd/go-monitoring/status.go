package main

import (
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

func printStatus(cfg config.Config) error {
	baseURL, err := statusBaseURL(cfg.Listen)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 5 * time.Second}
	var health healthResponse
	healthCode, err := getJSON(client, baseURL+"/healthz", &health)
	if err != nil {
		return err
	}
	var meta apimodel.MetaResponse
	metaCode, err := getJSON(client, baseURL+"/api/v1/meta", &meta)
	if err != nil {
		return err
	}

	fmt.Printf("Agent: %s\n", baseURL)
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

func getJSON(client *http.Client, url string, target any) (int, error) {
	resp, err := client.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return resp.StatusCode, err
	}
	return resp.StatusCode, nil
}

func statusBaseURL(listen string) (string, error) {
	addr := app.GetAddress(listen)
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	if strings.Contains(host, ":") {
		host = "[" + strings.Trim(host, "[]") + "]"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}
