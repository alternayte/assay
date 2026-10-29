// Command assay is the Assay server and its operator commands.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/alternayte/assay/internal/app"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/users"
)

// version is set at release time.
var version = "dev"

const usage = `Usage: assay <command>

Commands:
  serve                  Start the server. ASSAY_ROLE selects web, worker or all.
  migrate                Apply the database migrations and exit.
  admin create           Create an admin: --email, --name, optional --password.
  admin reset-password   Set the password of a user: --email, optional --password.
  version                Print the version.

Without --password, the command prints a generated password once.
`

func main() {
	os.Exit(run(os.Args[1:], os.Environ(), os.Stdout, os.Stderr))
}

func run(args, environ []string, stdout, stderr io.Writer) int {
	app.Version = version
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "serve", "migrate", "admin":
	default:
		fmt.Fprintf(stderr, "assay: unknown command %q\n\n%s", args[0], usage)
		return 2
	}

	cfg, err := app.LoadConfig(environ, app.NewLogger(stderr, slog.LevelInfo))
	if err != nil {
		fmt.Fprintf(stderr, "assay: the configuration is not valid:\n%v\n", err)
		return 1
	}
	log := app.NewLogger(stderr, cfg.LogLevel)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch args[0] {
	case "admin":
		return admin(ctx, cfg, log, args[1:], stdout, stderr)
	case "migrate":
		pool, _, err := app.Open(ctx, cfg, log, auth.Options{})
		if err != nil {
			log.Error("migrate failed", "error", err.Error())
			return 1
		}
		pool.Close()
		log.Info("migrations are current")
		return 0
	}

	s, err := app.New(ctx, cfg, log, app.Options{})
	if err != nil {
		log.Error("start failed", "error", err.Error())
		return 1
	}
	defer s.Close()
	if err := s.Run(ctx); err != nil {
		log.Error("server stopped", "error", err.Error())
		return 1
	}
	return 0
}

func admin(ctx context.Context, cfg app.Config, log *slog.Logger, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "create" && args[0] != "reset-password") {
		fmt.Fprint(stderr, usage)
		return 2
	}
	fs := flag.NewFlagSet("assay admin "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	email := fs.String("email", "", "email of the user")
	name := fs.String("name", "", "display name of the new admin")
	password := fs.String("password", "", "password; empty generates one")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *email == "" || (args[0] == "create" && *name == "") {
		fmt.Fprintln(stderr, "assay: --email is required. admin create also needs --name.")
		return 2
	}
	pool, a, err := app.Open(ctx, cfg, log, auth.Options{})
	if err != nil {
		log.Error("start failed", "error", err.Error())
		return 1
	}
	defer pool.Close()

	var generated string
	if args[0] == "create" {
		generated, err = users.CreateAdmin(ctx, pool, a, *email, *name, *password)
	} else {
		generated, err = users.ResetPassword(ctx, pool, a, *email, *password)
	}
	if err != nil {
		fmt.Fprintf(stderr, "assay: %v\n", err)
		return 1
	}
	if generated != "" {
		fmt.Fprintf(stdout, "Password for %s (it shows only once): %s\n", *email, generated)
	} else {
		fmt.Fprintf(stdout, "Done for %s.\n", *email)
	}
	return 0
}
