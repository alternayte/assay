package app

import (
	"bytes"
	"encoding/base64"
	"log/slog"
	"strings"
	"testing"
)

func testKey() string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
}

func TestSCN_SET_004_BootstrapVariables(t *testing.T) {
	t.Run("missing secret key names it", func(t *testing.T) {
		_, err := LoadConfig([]string{"ASSAY_DATABASE_URL=postgres://x"}, slog.New(slog.DiscardHandler))
		if err == nil || !strings.Contains(err.Error(), "ASSAY_SECRET_KEY") {
			t.Fatalf("want an error that names ASSAY_SECRET_KEY, got %v", err)
		}
	})

	t.Run("missing database URL names it", func(t *testing.T) {
		_, err := LoadConfig([]string{"ASSAY_SECRET_KEY=" + testKey()}, slog.New(slog.DiscardHandler))
		if err == nil || !strings.Contains(err.Error(), "ASSAY_DATABASE_URL") {
			t.Fatalf("want an error that names ASSAY_DATABASE_URL, got %v", err)
		}
	})

	t.Run("secret key of wrong length fails", func(t *testing.T) {
		_, err := LoadConfig([]string{
			"ASSAY_DATABASE_URL=postgres://x",
			"ASSAY_SECRET_KEY=" + base64.StdEncoding.EncodeToString([]byte("short")),
		}, slog.New(slog.DiscardHandler))
		if err == nil || !strings.Contains(err.Error(), "ASSAY_SECRET_KEY") {
			t.Fatalf("want an error that names ASSAY_SECRET_KEY, got %v", err)
		}
	})

	t.Run("unknown variable logs a warning", func(t *testing.T) {
		var buf bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&buf, nil))
		cfg, err := LoadConfig([]string{
			"ASSAY_DATABASE_URL=postgres://x",
			"ASSAY_SECRET_KEY=" + testKey(),
			"ASSAY_FOO=1",
			"HOME=/tmp",
		}, log)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), `"level":"WARN"`) || !strings.Contains(buf.String(), "ASSAY_FOO") {
			t.Fatalf("want a warning that names ASSAY_FOO, got %q", buf.String())
		}
		if strings.Contains(buf.String(), "HOME") {
			t.Fatalf("a variable without the ASSAY_ prefix must not warn: %q", buf.String())
		}
		if cfg.Role != RoleAll || cfg.Listen != ":8080" {
			t.Fatalf("defaults: got role %q listen %q", cfg.Role, cfg.Listen)
		}
	})

	t.Run("unknown role fails", func(t *testing.T) {
		_, err := LoadConfig([]string{
			"ASSAY_DATABASE_URL=postgres://x",
			"ASSAY_SECRET_KEY=" + testKey(),
			"ASSAY_ROLE=batch",
		}, slog.New(slog.DiscardHandler))
		if err == nil || !strings.Contains(err.Error(), "ASSAY_ROLE") {
			t.Fatalf("want an error that names ASSAY_ROLE, got %v", err)
		}
	})
}
