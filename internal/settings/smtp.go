package settings

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/secrets"
)

// SMTPConfig is the SMTP setting. The password is the secret smtp.password.
type SMTPConfig struct {
	Host     string `json:"host" doc:"Empty turns email off."`
	Port     int    `json:"port"`
	Username string `json:"username"`
	From     string `json:"from"`
	Security string `json:"security" enum:"starttls,tls,none"`
}

// SMTP is the definition of the SMTP setting.
var SMTP = Define("smtp", "Outgoing email. Assay sends invite links when a host is set.",
	SMTPConfig{Port: 587, Security: "starttls"},
	func(c SMTPConfig) error {
		if c.Host == "" {
			return nil
		}
		if c.Port < 1 || c.Port > 65535 {
			return errors.New("port must be 1 to 65535")
		}
		if _, err := mail.ParseAddress(c.From); err != nil {
			return errors.New("from must be an email address")
		}
		switch c.Security {
		case "starttls", "tls", "none":
		default:
			return errors.New("security must be starttls, tls or none")
		}
		return nil
	})

// SMTPPasswordSecret is the name of the secret that holds the SMTP password.
const SMTPPasswordSecret = "smtp.password"

// Mailer sends email with the SMTP setting.
type Mailer struct {
	Service *Service
	Secrets *secrets.Store
}

// Send sends a plain text message. It reports false and sends nothing when
// no SMTP host is set.
func (m *Mailer) Send(ctx context.Context, to, subject, body string) (bool, error) {
	cfg, err := Read[SMTPConfig](ctx, m.Service.Pool, SMTP)
	if err != nil {
		return false, err
	}
	if cfg.Host == "" {
		return false, nil
	}
	password, err := m.Secrets.Get(ctx, SMTPPasswordSecret)
	if err != nil && !errors.Is(err, secrets.ErrNotFound) {
		return false, err
	}
	return true, m.send(ctx, cfg, string(password), to, subject, body)
}

func (m *Mailer) send(ctx context.Context, cfg SMTPConfig, password, to, subject, body string) error {
	if _, err := mail.ParseAddress(to); err != nil {
		return errors.New("the recipient is not an email address")
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}

	var conn net.Conn
	var err error
	d := &net.Dialer{}
	if cfg.Security == "tls" {
		conn, err = (&tls.Dialer{NetDialer: d, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = c.Close() }()
	if cfg.Security == "starttls" {
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, password, cfg.Host)); err != nil {
			return fmt.Errorf("authenticate: %w", err)
		}
	}
	if err := c.Mail(cfg.From); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	msg := "From: " + cfg.From + "\r\nTo: " + to + "\r\nSubject: " + mimeHeader(subject) +
		"\r\nDate: " + time.Now().UTC().Format(time.RFC1123Z) +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" +
		strings.ReplaceAll(body, "\n", "\r\n")
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func mimeHeader(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

// RegisterSMTP adds the SMTP password and test routes.
func (m *Mailer) RegisterSMTP(a huma.API) {
	admins := huma.Middlewares{api.Require(auth.RoleAdmin)}

	type passwordInput struct {
		Body struct {
			Password string `json:"password" doc:"An empty value removes the password." maxLength:"1024"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "setSMTPPassword", Method: http.MethodPut, Path: "/api/v1/settings/smtp/password", Tags: []string{"settings"},
		Summary: "Set the SMTP password", Description: "The password is write-only.", DefaultStatus: http.StatusNoContent, Middlewares: admins,
	}, func(ctx context.Context, in *passwordInput) (*struct{}, error) {
		err := pgx.BeginFunc(ctx, m.Service.Pool, func(tx pgx.Tx) error {
			var err error
			if in.Body.Password == "" {
				err = m.Secrets.Delete(ctx, tx, SMTPPasswordSecret)
			} else {
				err = m.Secrets.Put(ctx, tx, SMTPPasswordSecret, []byte(in.Body.Password))
			}
			if err != nil {
				return err
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "settings.secret", Target: "secret:" + SMTPPasswordSecret})
		})
		if err != nil {
			return nil, api.Internal(err)
		}
		return nil, nil
	})

	type testInput struct {
		Body struct {
			To string `json:"to" format:"email"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "testSMTP", Method: http.MethodPost, Path: "/api/v1/settings/smtp/test", Tags: []string{"settings"},
		Summary: "Send a test email", DefaultStatus: http.StatusNoContent, Middlewares: admins,
	}, func(ctx context.Context, in *testInput) (*struct{}, error) {
		sent, err := m.Send(ctx, in.Body.To, "Assay test email", "This is a test email from Assay. The SMTP setting works.\n")
		if err != nil {
			return nil, api.Problem(http.StatusBadGateway, "smtp_failed", "The test email failed: "+err.Error())
		}
		if !sent {
			return nil, api.Problem(http.StatusConflict, "smtp_off", "Set an SMTP host first.")
		}
		return nil, nil
	})
}
