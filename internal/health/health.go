// Package health provides functions to check and update the health of the agent.
// It uses a file in shared memory or the temp directory to store the timestamp
// of the last successful persisted collection tick. If the timestamp is older than 90
// seconds, the agent is considered unhealthy.
package health

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"
)

const healthFilename = "go_monitoring_health"

// healthFile is the path to the health file.
var healthFile = getHealthFilePath()

const unhealthyAfter = 91 * time.Second

type Status struct {
	LastUpdated time.Time `json:"last_updated"`
	Healthy     bool      `json:"healthy"`
	Age         time.Duration
}

func getHealthFilePath() string {
	return healthFilePath("/dev/shm", os.TempDir())
}

func healthFilePath(preferredDir, fallbackDir string) string {
	if isDirectory(preferredDir) {
		return filepath.Join(preferredDir, healthFilename)
	}
	return filepath.Join(fallbackDir, healthFilename)
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func FilePath() string {
	return healthFile
}

func updateHealthFile(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func GetStatus() (Status, error) {
	fileInfo, err := os.Stat(healthFile)
	if err != nil {
		return Status{}, err
	}
	age := time.Since(fileInfo.ModTime())
	return Status{
		LastUpdated: fileInfo.ModTime(),
		Healthy:     age <= unhealthyAfter,
		Age:         age,
	}, nil
}

// Check checks if the latest persisted collection tick is still fresh.
func Check() error {
	status, err := GetStatus()
	if err != nil {
		return err
	}
	if !status.Healthy {
		log.Println("over 90 seconds since last successful persist")
		return errors.New("unhealthy")
	}
	return nil
}

// Update updates the modification time of the health file.
func Update() error {
	return updateHealthFile(healthFile)
}

// CleanUp removes the health file
func CleanUp() error {
	return os.Remove(healthFile)
}
