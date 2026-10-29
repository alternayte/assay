package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/assay/internal/auth"
)

// OpenAPIDocument returns the OpenAPI document of the API as indented JSON.
// It needs no database.
func OpenAPIDocument() ([]byte, error) {
	// The pool never connects. The services only keep it.
	pool, err := pgxpool.New(context.Background(), "postgres://127.0.0.1:1/none")
	if err != nil {
		return nil, err
	}
	defer pool.Close()
	log := slog.New(slog.DiscardHandler)
	cfg := Config{SecretKey: bytes.Repeat([]byte{0}, 32), BaseURL: "http://localhost:8080"}
	a, err := auth.New(pool, cfg.BaseURL, log, auth.Options{})
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, log: log, pool: pool, auth: a}
	if err := s.services(); err != nil {
		return nil, err
	}
	api, err := s.newAPI(chi.NewRouter())
	if err != nil {
		return nil, err
	}
	s.registerAPI(api)
	b, err := json.MarshalIndent(api.OpenAPI(), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
