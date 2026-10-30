// Command assay is the Assay server and its operator commands.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alternayte/assay/internal/app"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/doctor"
	"github.com/alternayte/assay/internal/eval"
	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/secrets"
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
  doctor                 Check the database, the secret key, the proxy, the CA
                         certificates and each endpoint. Exits 1 on a failure.
  secrets rekey          Encrypt all secrets again with ASSAY_SECRET_KEY_NEW.
                         Then set ASSAY_SECRET_KEY to that key and restart.
  fixtures serve         Serve the fake model API and the fake source APIs for
                         tests and the demo: --addr, default :8090.
  eval                   Ask a question set and write a Markdown report:
                         --url, --token, --file, optional --out (default
                         stdout) and --title.
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
	case "fixtures":
		return fixturesServe(args[1:], stderr)
	case "eval":
		return runEval(args[1:], stdout, stderr)
	case "serve", "migrate", "admin", "secrets", "doctor":
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
	case "secrets":
		return rekey(ctx, cfg, log, args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(ctx, cfg, stdout)
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

func rekey(ctx context.Context, cfg app.Config, log *slog.Logger, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "rekey" {
		fmt.Fprint(stderr, usage)
		return 2
	}
	if cfg.SecretKeyNew == nil {
		fmt.Fprintln(stderr, "assay: ASSAY_SECRET_KEY_NEW is not set. Set it to the new key: 32 random bytes in base64.")
		return 2
	}
	current, err := secrets.NewBox(cfg.SecretKey)
	if err != nil {
		fmt.Fprintf(stderr, "assay: %v\n", err)
		return 1
	}
	next, err := secrets.NewBox(cfg.SecretKeyNew)
	if err != nil {
		fmt.Fprintf(stderr, "assay: %v\n", err)
		return 1
	}
	pool, _, err := app.Open(ctx, cfg, log, auth.Options{})
	if err != nil {
		log.Error("start failed", "error", err.Error())
		return 1
	}
	defer pool.Close()
	n, err := secrets.Rekey(ctx, pool, current, next)
	if err != nil {
		fmt.Fprintf(stderr, "assay: rekey failed. No secret changed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Encrypted %d secrets with the new key. Set ASSAY_SECRET_KEY to the value of ASSAY_SECRET_KEY_NEW, remove ASSAY_SECRET_KEY_NEW and restart.\n", n)
	return 0
}

func runDoctor(ctx context.Context, cfg app.Config, stdout io.Writer) int {
	s, err := app.New(ctx, cfg, app.NewLogger(io.Discard, slog.LevelError), app.Options{})
	if err != nil {
		fmt.Fprintf(stdout, "FAIL  Database: %v\n", err)
		return 1
	}
	defer s.Close()
	checks := s.Doctor().Run(ctx)
	for _, c := range checks {
		mark := "PASS"
		if !c.OK {
			mark = "FAIL"
		}
		fmt.Fprintf(stdout, "%s  %s: %s\n", mark, c.Name, c.Reason)
	}
	if doctor.Failed(checks) {
		return 1
	}
	return 0
}

func fixturesServe(args []string, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "serve" {
		fmt.Fprint(stderr, usage)
		return 2
	}
	fs := flag.NewFlagSet("assay fixtures serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", ":8090", "listen address")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	log := app.NewLogger(stderr, slog.LevelInfo)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := fixtures.Serve(ctx, *addr, log); err != nil {
		log.Error("fixtures stopped", "error", err.Error())
		return 1
	}
	return 0
}

// runEval asks each question of a set with an access token and writes the
// report (REQ-EVL-003).
func runEval(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("assay eval", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("url", "", "the URL of the Assay instance")
	token := fs.String("token", "", "an access token; ASSAY_TOKEN when empty")
	file := fs.String("file", "", "the question set, such as evals/questions.yaml")
	out := fs.String("out", "", "the report file; empty writes to stdout")
	title := fs.String("title", "Assay evaluation", "the report title")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *token == "" {
		*token = os.Getenv("ASSAY_TOKEN")
	}
	if *base == "" || *token == "" || *file == "" {
		fmt.Fprintln(stderr, "assay eval: give --url, --token and --file")
		return 2
	}
	qs, err := eval.Load(*file)
	if err != nil {
		fmt.Fprintf(stderr, "assay eval: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	report := eval.Score(eval.Run(ctx, &http.Client{Timeout: 2 * time.Minute}, *base, *token, qs)).Markdown(*title)
	if *out == "" {
		fmt.Fprint(stdout, report)
		return 0
	}
	if err := os.WriteFile(*out, []byte(report), 0o644); err != nil { //nolint:gosec // A report holds no secret.
		fmt.Fprintf(stderr, "assay eval: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "assay eval: %d questions, report in %s\n", len(qs), *out)
	return 0
}
