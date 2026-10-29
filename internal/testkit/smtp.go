package testkit

import (
	"bufio"
	"net"
	"strings"
	"sync"
	"testing"
)

// Mail is one message that the fake SMTP server received.
type Mail struct {
	From string
	To   []string
	Data string
}

// SMTPServer is a fake SMTP server without TLS or authentication.
type SMTPServer struct {
	Addr string
	mu   sync.Mutex
	mail []Mail
}

// Messages returns the received messages.
func (s *SMTPServer) Messages() []Mail {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Mail(nil), s.mail...)
}

// StartSMTP runs a fake SMTP server until the test ends.
func StartSMTP(t testing.TB) *SMTPServer {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &SMTPServer{Addr: ln.Addr().String()}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *SMTPServer) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	r := bufio.NewReader(c)
	reply := func(line string) { _, _ = c.Write([]byte(line + "\r\n")) }
	reply("220 fake ESMTP")
	var m Mail
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			reply("250 fake")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			m = Mail{From: strings.Trim(strings.TrimSpace(line)[10:], "<>")}
			reply("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			m.To = append(m.To, strings.Trim(strings.TrimSpace(line)[8:], "<>"))
			reply("250 ok")
		case cmd == "DATA":
			reply("354 end with .")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			m.Data = b.String()
			s.mu.Lock()
			s.mail = append(s.mail, m)
			s.mu.Unlock()
			reply("250 queued")
		case cmd == "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}
