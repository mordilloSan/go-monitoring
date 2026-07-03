package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/mordilloSan/go-monitoring/cmd"
)

func main() {
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cmd.Run(ctx, os.Args)
	stopSignals()
	os.Exit(code)
}
