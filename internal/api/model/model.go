// Package model contains the JSON response contracts served by the HTTP API.
package model

import (
	"github.com/mordilloSan/go-monitoring/internal/domain/container"
	"github.com/mordilloSan/go-monitoring/internal/domain/smart"
	"github.com/mordilloSan/go-monitoring/internal/domain/system"
)

type SummaryResponse struct {
	CapturedAt int64 `json:"captured_at"`
	system.CombinedData
}

type SystemSummaryResponse struct {
	CapturedAt int64 `json:"captured_at"`
	system.Summary
}

type HistoryItem[T any] struct {
	CapturedAt int64 `json:"captured_at"`
	Stats      T     `json:"stats"`
}

type HistoryResponse[T any] struct {
	Resolution string           `json:"resolution"`
	Items      []HistoryItem[T] `json:"items"`
}

type CurrentItemsResponse[T any] struct {
	CapturedAt int64 `json:"captured_at"`
	Items      []T   `json:"items"`
}

type CurrentDataResponse[T any] struct {
	CapturedAt int64 `json:"captured_at"`
	Data       T     `json:"data"`
}

type ContainerCurrent struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	Image       string                 `json:"image,omitempty"`
	Ports       string                 `json:"ports,omitempty"`
	Status      string                 `json:"status,omitempty"`
	Health      container.DockerHealth `json:"health,omitempty"`
	Cpu         float64                `json:"cpu_percent"`
	Mem         float64                `json:"memory_mb"`
	NetworkSent float64                `json:"network_sent_mb,omitempty,omitzero"`
	NetworkRecv float64                `json:"network_recv_mb,omitempty,omitzero"`
	Bandwidth   [2]uint64              `json:"bandwidth_bytes,omitempty,omitzero"`
}

type SmartDeviceCurrent struct {
	ID   string          `json:"id"`
	Key  string          `json:"key"`
	Data smart.SmartData `json:"data"`
}

type MetaResponse struct {
	Version              string            `json:"version"`
	DataDir              string            `json:"data_dir"`
	DBPath               string            `json:"db_path"`
	ListenAddr           string            `json:"listen_addr"`
	CollectorInterval    string            `json:"collector_interval"`
	SmartRefreshInterval string            `json:"smart_refresh_interval"`
	Retention            map[string]string `json:"retention"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}
