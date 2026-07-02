//go:build testing

package app

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUnescapeServiceName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"nginx.service", "nginx.service"},                                     // No escaping needed
		{"test\\x2dwith\\x2ddashes.service", "test-with-dashes.service"},       // \x2d is dash
		{"service\\x20with\\x20spaces.service", "service with spaces.service"}, // \x20 is space
		{"mixed\\x2dand\\x2dnormal", "mixed-and-normal"},                       // Mixed escaped and normal
		{"no-escape-here", "no-escape-here"},                                   // No escape sequences
		{"", ""},                                                               // Empty string
		{"\\x2d\\x2d", "--"},                                                   // Multiple escapes
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			result := unescapeServiceName(test.input)
			assert.Equal(t, test.expected, result)
		})
	}
}

func TestUnescapeServiceNameInvalid(t *testing.T) {
	// Test invalid escape sequences - should return original string
	invalidInputs := []string{
		"invalid\\x",   // Incomplete escape
		"invalid\\xZZ", // Invalid hex
		"invalid\\x2",  // Incomplete hex
		"invalid\\xyz", // Not a valid escape
	}

	for _, input := range invalidInputs {
		t.Run(input, func(t *testing.T) {
			result := unescapeServiceName(input)
			assert.Equal(t, input, result, "Invalid escape sequences should return original string")
		})
	}
}

func TestIsSystemdAvailable(t *testing.T) {
	// Note: This test's result will vary based on the actual system running the tests
	// On systems with systemd, it should return true
	// On systems without systemd, it should return false
	result := isSystemdAvailable()

	// Check if either the /run/systemd/system directory exists or PID 1 is systemd
	runSystemdExists := false
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		runSystemdExists = true
	}

	pid1IsSystemd := false
	if data, err := os.ReadFile("/proc/1/comm"); err == nil {
		pid1IsSystemd = strings.TrimSpace(string(data)) == "systemd"
	}

	expected := runSystemdExists || pid1IsSystemd

	assert.Equal(t, expected, result, "isSystemdAvailable should correctly detect systemd presence")

	// Log the result for informational purposes
	if result {
		t.Log("Systemd is available on this system")
	} else {
		t.Log("Systemd is not available on this system")
	}
}

func TestGetServicePatterns(t *testing.T) {
	tests := []struct {
		name     string
		env      string
		expected []string
	}{
		{
			name:     "default when no env var set",
			env:      "",
			expected: []string{"*.service"},
		},
		{
			name:     "single pattern",
			env:      "nginx",
			expected: []string{"nginx.service"},
		},
		{
			name:     "multiple patterns",
			env:      "nginx,apache,postgresql",
			expected: []string{"nginx.service", "apache.service", "postgresql.service"},
		},
		{
			name:     "patterns with .service suffix",
			env:      "nginx.service,apache.service",
			expected: []string{"nginx.service", "apache.service"},
		},
		{
			name:     "mixed patterns with and without suffix",
			env:      "nginx.service,apache,postgresql.service",
			expected: []string{"nginx.service", "apache.service", "postgresql.service"},
		},
		{
			name:     "patterns with whitespace",
			env:      " nginx , apache , postgresql ",
			expected: []string{"nginx.service", "apache.service", "postgresql.service"},
		},
		{
			name:     "empty patterns are skipped",
			env:      "nginx,,apache,  ,postgresql",
			expected: []string{"nginx.service", "apache.service", "postgresql.service"},
		},
		{
			name:     "wildcard pattern",
			env:      "*nginx*,*apache*",
			expected: []string{"*nginx*.service", "*apache*.service"},
		},
		{
			name:     "opt into timer monitoring",
			env:      "nginx.service,docker,apache.timer",
			expected: []string{"nginx.service", "docker.service", "apache.timer"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env != "" {
				t.Setenv("SERVICE_PATTERNS", tt.env)
			}

			result := getServicePatterns()

			assert.Equal(t, tt.expected, result, "Patterns should match expected values")
		})
	}
}
