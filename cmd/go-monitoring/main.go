package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/mordilloSan/go-monitoring/internal/app"
	"github.com/mordilloSan/go-monitoring/internal/health"
	buildinfo "github.com/mordilloSan/go-monitoring/internal/version"
	"github.com/spf13/pflag"
)

type cmdOptions struct {
	listen     string
	history    string
	historySet bool
}

func (opts *cmdOptions) parse() bool {
	subcommand := ""
	if len(os.Args) > 1 {
		subcommand = os.Args[1]
	}

	if subcommand == "health" {
		if err := health.Check(); err != nil {
			log.Fatal(err)
		}
		fmt.Print("ok")
		return true
	}

	pflag.StringVarP(&opts.listen, "listen", "l", "", "Address or port to listen on")
	pflag.StringVar(&opts.history, "history", "", "Comma-separated history plugin allowlist, or all/none")
	version := pflag.BoolP("version", "v", false, "Show version information")
	help := pflag.BoolP("help", "h", false, "Show this help message")

	for i, arg := range os.Args {
		switch {
		case arg == "-listen":
			os.Args[i] = "--listen"
		case strings.HasPrefix(arg, "-listen="):
			os.Args[i] = "--listen" + arg[len("-listen"):]
		}
	}

	pflag.Usage = func() {
		builder := strings.Builder{}
		builder.WriteString("Usage: ")
		builder.WriteString(os.Args[0])
		builder.WriteString(" [command] [flags]\n")
		builder.WriteString("\nCommands:\n")
		builder.WriteString("  health       Check if the latest persisted collection tick is fresh\n")
		builder.WriteString("\nFlags:\n")
		fmt.Print(builder.String())
		pflag.PrintDefaults()
	}

	pflag.Parse()
	opts.historySet = pflag.Lookup("history").Changed

	switch {
	case *version:
		fmt.Println(buildinfo.RepoName+"-agent", buildinfo.Version)
		return true
	case *help || subcommand == "help":
		pflag.Usage()
		return true
	}

	return false
}

func main() {
	var opts cmdOptions
	if opts.parse() {
		return
	}

	a, err := app.New()
	if err != nil {
		log.Fatal("Failed to create agent: ", err)
	}

	if err := a.Start(app.RunOptions{
		Addr:       app.GetAddress(opts.listen),
		History:    opts.history,
		HistorySet: opts.historySet,
	}); err != nil {
		log.Fatal("Failed to start standalone agent: ", err)
	}
}
