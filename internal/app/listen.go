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
		return ":45876"
	}
	if !strings.Contains(addr, ":") {
		addr = ":" + addr
	}
	return addr
}
