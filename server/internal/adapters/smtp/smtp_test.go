package smtp_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"io"
	"math/big"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pmoscode/SerMon/server/internal/adapters/smtp"
)

// testPassword is a fixture of the fake server, not a secret.
const testPassword = "fixture-password" // betterleaks:allow

// fakeServer is a minimal SMTP server: EHLO, optional STARTTLS, AUTH PLAIN, MAIL, RCPT, DATA.
type fakeServer struct {
	t         *testing.T
	ln        net.Listener
	cert      tls.Certificate
	startTLS  bool // offer STARTTLS
	implicit  bool // TLS from the first byte
	hang      bool // never answer
	mu        sync.Mutex
	commands  []string
	auth      string // decoded AUTH PLAIN
	data      string
	plainAuth bool // AUTH happened before TLS
}

func newFakeServer(t *testing.T, configure func(*fakeServer)) (*fakeServer, *x509.CertPool) {
	t.Helper()
	cert, pool := testCert(t)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeServer{t: t, ln: ln, cert: cert}
	configure(f)
	if f.implicit {
		f.ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	}
	t.Cleanup(func() { _ = ln.Close() })
	go f.serve()
	return f, pool
}

func (f *fakeServer) port() int {
	addr, _ := f.ln.Addr().(*net.TCPAddr)
	return addr.Port
}

func (f *fakeServer) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *fakeServer) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	if f.hang {
		_, _ = io.Copy(io.Discard, conn)
		return
	}
	secure := f.implicit
	r, w := bufio.NewReader(conn), conn
	say := func(line string) { _, _ = io.WriteString(w, line+"\r\n") }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		f.mu.Lock()
		f.commands = append(f.commands, strings.SplitN(line, " ", 2)[0])
		f.mu.Unlock()
		switch verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0]); verb {
		case "EHLO":
			say("250-fake")
			if f.startTLS && !secure {
				say("250-STARTTLS")
			}
			say("250 AUTH PLAIN")
		case "STARTTLS":
			say("220 go ahead")
			tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{f.cert}, MinVersion: tls.VersionTLS12})
			if err := tlsConn.HandshakeContext(context.Background()); err != nil {
				return
			}
			conn, secure = tlsConn, true
			r, w = bufio.NewReader(tlsConn), tlsConn
		case "AUTH":
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
			f.mu.Lock()
			f.auth, f.plainAuth = string(raw), !secure
			f.mu.Unlock()
			say("235 ok")
		case "MAIL", "RCPT", "RSET", "NOOP":
			say("250 ok")
		case "DATA":
			say("354 go on")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, ".")) // undo dot-stuffing (RFC 5321 4.5.2)
			}
			f.mu.Lock()
			f.data = b.String()
			f.mu.Unlock()
			say("250 queued")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("502 unknown")
		}
	}
}

func testCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake smtp"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func mailer(f *fakeServer, pool *x509.CertPool, mode smtp.TLSMode) *smtp.Mailer {
	return smtp.New(smtp.Config{
		Host: "127.0.0.1", Port: f.port(), TLS: mode, From: "sermon@example.com", FromName: "SerMon Ü",
		Username: "sermon", Password: testPassword, RootCAs: pool, Timeout: 2 * time.Second,
	})
}

var message = smtp.Message{To: "bea@example.org", Subject: "Einladung zu „Alpha“", Text: "Hallo Bea,\nbitte annehmen: https://sermon.example.com/invite#token\n.\nEnde"}

func TestMailer_SendsOverSTARTTLSWithAuthentication(t *testing.T) {
	f, pool := newFakeServer(t, func(f *fakeServer) { f.startTLS = true })

	if err := mailer(f, pool, smtp.StartTLS).Send(context.Background(), message); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.plainAuth || f.auth != "\x00sermon\x00"+testPassword {
		t.Errorf("auth = %q (before TLS: %v), want credentials only after STARTTLS", f.auth, f.plainAuth)
	}
	msg, err := mail.ReadMessage(strings.NewReader(f.data))
	if err != nil {
		t.Fatal(err)
	}
	dec := new(mime.WordDecoder)
	subject, _ := dec.DecodeHeader(msg.Header.Get("Subject"))
	from, _ := mail.ParseAddress(msg.Header.Get("From"))
	if subject != message.Subject || from.Address != "sermon@example.com" || from.Name != "SerMon Ü" || msg.Header.Get("To") != "bea@example.org" {
		t.Errorf("headers = %v (subject %q, from %+v)", msg.Header, subject, from)
	}
	if msg.Header.Get("Message-Id") == "" || msg.Header.Get("Date") == "" || !strings.HasPrefix(msg.Header.Get("Content-Type"), "text/plain; charset=utf-8") {
		t.Errorf("missing standard headers: %v", msg.Header)
	}
	body, _ := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if strings.TrimSuffix(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") != message.Text {
		t.Errorf("body = %q, want %q (a lone dot must survive)", body, message.Text)
	}
}

// Without STARTTLS the credentials and the invitation link would travel in clear text: the
// mailer gives up before AUTH and MAIL.
func TestMailer_RefusesAServerWithoutSTARTTLS(t *testing.T) {
	f, pool := newFakeServer(t, func(*fakeServer) {})

	err := mailer(f, pool, smtp.StartTLS).Send(context.Background(), message)

	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("Send() error = %v, want a STARTTLS error", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.commands {
		if c == "AUTH" || c == "MAIL" {
			t.Errorf("sent %s without TLS", c)
		}
	}
}

func TestMailer_SendsOverImplicitTLS(t *testing.T) {
	f, pool := newFakeServer(t, func(f *fakeServer) { f.implicit = true })
	if err := mailer(f, pool, smtp.ImplicitTLS).Send(context.Background(), message); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
}

func TestMailer_VerifiesTheServerCertificate(t *testing.T) {
	f, _ := newFakeServer(t, func(f *fakeServer) { f.startTLS = true })
	_, otherPool := testCert(t)
	if err := mailer(f, otherPool, smtp.StartTLS).Send(context.Background(), message); err == nil {
		t.Error("an untrusted certificate must fail")
	}
}

// Development with Mailpit: plain SMTP, no authentication.
func TestMailer_SendsPlainWithoutAuthInDevelopment(t *testing.T) {
	f, _ := newFakeServer(t, func(*fakeServer) {})
	m := smtp.New(smtp.Config{Host: "127.0.0.1", Port: f.port(), TLS: smtp.NoTLS, From: "sermon@localhost", Timeout: 2 * time.Second})
	if err := m.Send(context.Background(), message); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
}

func TestMailer_RejectsHeaderInjection(t *testing.T) {
	m := smtp.New(smtp.Config{Host: "127.0.0.1", Port: 1, TLS: smtp.NoTLS, From: "sermon@localhost"})
	for _, msg := range []smtp.Message{
		{To: "bea@example.org\r\nBcc: eve@example.org", Subject: "x", Text: "x"},
		{To: "not an address", Subject: "x", Text: "x"},
		{To: "bea@example.org", Subject: "x\r\nBcc: eve@example.org", Text: "x"},
	} {
		if err := m.Send(context.Background(), msg); !errors.Is(err, smtp.ErrInvalidMessage) {
			t.Errorf("Send(%q / %q) = %v, want ErrInvalidMessage", msg.To, msg.Subject, err)
		}
	}
}

func TestMailer_GivesUpOnAHangingServer(t *testing.T) {
	f, pool := newFakeServer(t, func(f *fakeServer) { f.hang = true })
	m := smtp.New(smtp.Config{Host: "127.0.0.1", Port: f.port(), TLS: smtp.StartTLS, From: "s@example.com", RootCAs: pool, Timeout: 300 * time.Millisecond})
	started := time.Now()
	if err := m.Send(context.Background(), message); err == nil {
		t.Fatal("Send() to a hanging server succeeded")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("Send() took %v, want the timeout", elapsed)
	}
}
