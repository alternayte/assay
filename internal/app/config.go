package app

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// Role selects the parts of the process that run.
type Role string

const (
	RoleAll    Role = "all"
	RoleWeb    Role = "web"
	RoleWorker Role = "worker"
)

// Config is the bootstrap configuration. Everything else is a setting in the
// database.
type Config struct {
	DatabaseURL  string
	SecretKey    []byte
	SecretKeyNew []byte
	BaseURL      string
	Listen       string
	Role         Role
	SetupToken   string
	ExtraCAPEM   string
	ExtraCAFile  string
	Demo         bool
	LogLevel     slog.Level
}

var knownVars = map[string]bool{
	"ASSAY_DATABASE_URL":   true,
	"ASSAY_SECRET_KEY":     true,
	"ASSAY_SECRET_KEY_NEW": true,
	"ASSAY_BASE_URL":       true,
	"ASSAY_LISTEN":         true,
	"ASSAY_ROLE":           true,
	"ASSAY_SETUP_TOKEN":    true,
	"ASSAY_EXTRA_CA_PEM":   true,
	"ASSAY_EXTRA_CA_FILE":  true,
	"ASSAY_DEMO":           true,
	"ASSAY_LOG_LEVEL":      true,
}

// LoadConfig reads the bootstrap variables from environ. It fails when a
// required variable is missing or invalid, and it warns about an unknown
// ASSAY_ variable.
func LoadConfig(environ []string, log *slog.Logger) (Config, error) {
	env := map[string]string{}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		env[k] = v
		if strings.HasPrefix(k, "ASSAY_") && !knownVars[k] {
			log.Warn("unknown variable is ignored", "variable", k)
		}
	}

	cfg := Config{
		DatabaseURL: env["ASSAY_DATABASE_URL"],
		BaseURL:     strings.TrimRight(valueOr(env["ASSAY_BASE_URL"], "http://localhost:8080"), "/"),
		Listen:      valueOr(env["ASSAY_LISTEN"], ":8080"),
		Role:        Role(valueOr(env["ASSAY_ROLE"], string(RoleAll))),
		SetupToken:  env["ASSAY_SETUP_TOKEN"],
		ExtraCAPEM:  env["ASSAY_EXTRA_CA_PEM"],
		ExtraCAFile: env["ASSAY_EXTRA_CA_FILE"],
		Demo:        env["ASSAY_DEMO"] == "true" || env["ASSAY_DEMO"] == "1",
	}

	var errs []error
	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("ASSAY_DATABASE_URL is not set. Set it to the PostgreSQL connection URL"))
	}
	key, err := decodeKey("ASSAY_SECRET_KEY", env["ASSAY_SECRET_KEY"], true)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.SecretKey = key
	keyNew, err := decodeKey("ASSAY_SECRET_KEY_NEW", env["ASSAY_SECRET_KEY_NEW"], false)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.SecretKeyNew = keyNew
	switch cfg.Role {
	case RoleAll, RoleWeb, RoleWorker:
	default:
		errs = append(errs, fmt.Errorf("ASSAY_ROLE is %q. Set it to all, web or worker", cfg.Role))
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(valueOr(env["ASSAY_LOG_LEVEL"], "info"))); err != nil {
		errs = append(errs, fmt.Errorf("ASSAY_LOG_LEVEL is %q. Set it to debug, info, warn or error", env["ASSAY_LOG_LEVEL"]))
	}
	return cfg, errors.Join(errs...)
}

// decodeKey reads a base64 key of 32 bytes.
func decodeKey(name, value string, required bool) ([]byte, error) {
	if value == "" {
		if required {
			return nil, fmt.Errorf("%s is not set. Set it to 32 random bytes in base64, for example the output of: openssl rand -base64 32", name)
		}
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%s must be 32 bytes in base64", name)
	}
	return key, nil
}

func valueOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
