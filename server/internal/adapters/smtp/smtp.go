// Package smtp sends mail over SMTP with TLS (M1-39; notifications follow with E2.4).
//
// The connection is always protected in production: STARTTLS is required (a server that doesn't
// offer it is refused before credentials or content are sent) or TLS starts with the first byte.
// Plain SMTP exists for development with Mailpit only (the configuration enforces that). Headers
// are built here from validated values; a line break in a recipient or subject is refused, never
// escaped.
package smtp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// TLSMode is how the connection is protected.
type TLSMode int

// TLS modes.
const (
	StartTLS    TLSMode = iota // STARTTLS required
	ImplicitTLS                // TLS from the first byte (port 465)
	NoTLS                      // plain, development only
)

// ErrInvalidMessage means a recipient or subject could break the message headers.
var ErrInvalidMessage = errors.New("invalid mail message")

// defaultTimeout bounds a whole delivery when the context has no earlier deadline.
const defaultTimeout = 30 * time.Second

// Config configures the Mailer.
type Config struct {
	Host     string
	Port     int
	TLS      TLSMode
	From     string // address
	FromName string // display name, may be empty
	Username string // empty: no authentication
	Password string
	RootCAs  *x509.CertPool // nil: system roots
	Timeout  time.Duration  // 0: defaultTimeout
}

// Message is a plain text mail to one recipient.
type Message struct {
	To      string
	Subject string
	Text    string
}

// Mailer sends messages.
type Mailer struct{ config Config }

// New returns a Mailer.
func New(config Config) *Mailer {
	if config.Timeout == 0 {
		config.Timeout = defaultTimeout
	}
	return &Mailer{config: config}
}

// SendText sends a plain text mail (invitation.Mailer).
func (m *Mailer) SendText(ctx context.Context, to, subject, text string) error {
	return m.Send(ctx, Message{To: to, Subject: subject, Text: text})
}

// Send delivers the message.
func (m *Mailer) Send(ctx context.Context, msg Message) error {
	data, err := m.compose(msg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, m.config.Timeout)
	defer cancel()
	if err := m.deliver(ctx, msg.To, data); err != nil {
		return fmt.Errorf("send mail via %s: %w", m.config.Host, err)
	}
	return nil
}

func (m *Mailer) deliver(ctx context.Context, to string, data []byte) error {
	addr := net.JoinHostPort(m.config.Host, strconv.Itoa(m.config.Port))
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return err //nolint:wrapcheck // wrapped by Send
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	// Closing the connection unblocks every read and write when the context ends.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	tlsConfig := &tls.Config{ServerName: m.config.Host, RootCAs: m.config.RootCAs, MinVersion: tls.VersionTLS12}
	if m.config.TLS == ImplicitTLS {
		tlsConn := tls.Client(conn, tlsConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return err //nolint:wrapcheck // wrapped by Send
		}
		conn = tlsConn
	}
	client, err := smtp.NewClient(conn, m.config.Host)
	if err != nil {
		return err //nolint:wrapcheck // wrapped by Send
	}
	defer func() { _ = client.Close() }()
	if m.config.TLS == StartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("the server offers no STARTTLS; refusing to send credentials or mail in clear text")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return err //nolint:wrapcheck // wrapped by Send
		}
	}
	if m.config.Username != "" {
		// PlainAuth refuses to send the password over a connection without TLS.
		if err := client.Auth(smtp.PlainAuth("", m.config.Username, m.config.Password, m.config.Host)); err != nil {
			return err //nolint:wrapcheck // wrapped by Send
		}
	}
	if err := client.Mail(m.config.From); err != nil {
		return err //nolint:wrapcheck // wrapped by Send
	}
	if err := client.Rcpt(to); err != nil {
		return err //nolint:wrapcheck // wrapped by Send
	}
	w, err := client.Data()
	if err != nil {
		return err //nolint:wrapcheck // wrapped by Send
	}
	if _, err := w.Write(data); err != nil {
		return err //nolint:wrapcheck // wrapped by Send
	}
	if err := w.Close(); err != nil {
		return err //nolint:wrapcheck // wrapped by Send
	}
	return client.Quit() //nolint:wrapcheck // wrapped by Send
}

// compose builds the RFC 5322 message: UTF-8 text, quoted-printable, encoded headers.
func (m *Mailer) compose(msg Message) ([]byte, error) {
	if strings.ContainsAny(msg.To, "\r\n") || strings.ContainsAny(msg.Subject, "\r\n") {
		return nil, ErrInvalidMessage
	}
	to, err := mail.ParseAddress(msg.To)
	if err != nil || to.Address != msg.To {
		return nil, ErrInvalidMessage
	}
	from := (&mail.Address{Name: m.config.FromName, Address: m.config.From}).String()
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, fmt.Errorf("message id: %w", err)
	}
	domain := m.config.From[strings.LastIndex(m.config.From, "@")+1:]
	var b bytes.Buffer
	for _, h := range [][2]string{
		{"From", from},
		{"To", to.Address},
		{"Subject", mime.QEncoding.Encode("utf-8", msg.Subject)},
		{"Date", time.Now().UTC().Format(time.RFC1123Z)},
		{"Message-ID", "<" + hex.EncodeToString(id) + "@" + domain + ">"},
		{"MIME-Version", "1.0"},
		{"Content-Type", "text/plain; charset=utf-8"},
		{"Content-Transfer-Encoding", "quoted-printable"},
		{"Auto-Submitted", "auto-generated"},
	} {
		b.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	if _, err := qp.Write([]byte(strings.ReplaceAll(strings.ReplaceAll(msg.Text, "\r\n", "\n"), "\n", "\r\n"))); err != nil {
		return nil, fmt.Errorf("encode body: %w", err)
	}
	if err := qp.Close(); err != nil {
		return nil, fmt.Errorf("encode body: %w", err)
	}
	return b.Bytes(), nil
}
