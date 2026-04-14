package main

import (
	"os"
	"testing"

	agent "github.com/mordilloSan/go-monitoring/internal/agent"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
)

func resetFlags(args []string) {
	os.Args = args
	pflag.CommandLine = pflag.NewFlagSet(args[0], pflag.ContinueOnError)
}

func TestGetAddress(t *testing.T) {
	tests := []struct {
		name     string
		listen   string
		envVars  map[string]string
		expected string
	}{
		{name: "default port", expected: ":45876"},
		{name: "port only", listen: "8080", expected: ":8080"},
		{name: "explicit address", listen: "127.0.0.1:9000", expected: "127.0.0.1:9000"},
		{
			name: "listen env",
			envVars: map[string]string{
				"LISTEN": "0.0.0.0:9001",
			},
			expected: "0.0.0.0:9001",
		},
		{
			name: "legacy port env",
			envVars: map[string]string{
				"PORT": "7000",
			},
			expected: ":7000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for key, value := range tt.envVars {
				t.Setenv(key, value)
			}
			assert.Equal(t, tt.expected, agent.GetAddress(tt.listen))
		})
	}
}

func TestParseFlags(t *testing.T) {
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	tests := []struct {
		name         string
		args         []string
		expected     cmdOptions
		handled      bool
		expectedArgs []string
	}{
		{
			name:     "no flags",
			args:     []string{"cmd"},
			expected: cmdOptions{},
		},
		{
			name:     "listen flag",
			args:     []string{"cmd", "--listen", "8080"},
			expected: cmdOptions{listen: "8080"},
		},
		{
			name:         "legacy single dash listen",
			args:         []string{"cmd", "-listen=8080"},
			expected:     cmdOptions{listen: "8080"},
			expectedArgs: []string{"cmd", "--listen=8080"},
		},
		{
			name:    "help command handled",
			args:    []string{"cmd", "help"},
			handled: true,
		},
		{
			name:    "version flag handled",
			args:    []string{"cmd", "--version"},
			handled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetFlags(tt.args)

			var opts cmdOptions
			handled := opts.parse()

			assert.Equal(t, tt.handled, handled)
			if !handled {
				assert.Equal(t, tt.expected, opts)
			}
			if tt.expectedArgs != nil {
				assert.Equal(t, tt.expectedArgs, os.Args)
			}
		})
	}
}
