// Command assay is the Assay server and its operator commands.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/alternayte/assay/internal/app"
)

// version is set at release time.
var version = "dev"

const usage = `Usage: assay <command>

Commands:
  serve      Start the server. ASSAY_ROLE selects web, worker or all.
  migrate    Apply the database migrations and exit.
  version    Print the version.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "serve", "migrate":
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "assay: unknown command %q\n\n%s", args[0], usage)
		return 2
	}

	boot := app.NewLogger(stderr, 0)
	cfg, err := app.LoadConfig(os.Environ(), boot)
	if err != nil {
		fmt.Fprintf(stderr, "assay: the configuration is not valid:\n%v\n", err)
		return 1
	}
	log := app.NewLogger(stderr, cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	s, err := app.New(ctx, cfg, log, app.Options{})
	if err != nil {
		log.Error("start failed", "error", err.Error())
		return 1
	}
	defer s.Close()
	if args[0] == "migrate" {
		log.Info("migrations are current")
		return 0
	}
	if err := s.Run(ctx); err != nil {
		log.Error("server stopped", "error", err.Error())
		return 1
	}
	return 0
}
