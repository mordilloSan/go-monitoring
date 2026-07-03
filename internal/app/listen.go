package app

import (
	"strings"

	"github.com/mordilloSan/go-monitoring/internal/utils"
)

func GetAddress(addr string) string {
	if addr == "" {
		addr, _ = utils.GetEnv("LISTEN")
	}
	if addr == "" {
		addr, _ = utils.GetEnv("PORT")
	}
	if addr == "" {
		return "127.0.0.1:45876"
	}
	// A bare port stays local-only; use an explicit host (or ":port") to
	// listen on all interfaces.
	if !strings.Contains(addr, ":") {
		addr = "127.0.0.1:" + addr
	}
	return addr
}
