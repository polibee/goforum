package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"

	"goforum/internal/platform/config"
	"goforum/internal/platform/settings"
)

type Message struct {
	To      []string
	Subject string
	HTML    string
	Text    string
}

type Mailer interface {
	Send(ctx context.Context, msg Message) error
}

// LogMailer is the default development mailer: it never sends real email.
type LogMailer struct{ log *slog.Logger }

func NewLogMailer(log *slog.Logger) *LogMailer { return &LogMailer{log: log} }

func (m *LogMailer) Send(_ context.Context, msg Message) error {
	m.log.Info("mail (log-only)", "to", msg.To, "subject", msg.Subject)
	return nil
}

// Dynamic resolves the active provider from settings on every send, so admins
// can switch between log, SMTP and Resend without restarting.
type Dynamic struct {
	log      *slog.Logger
	cfg      *config.Config
	settings *settings.Store
}

func NewDynamic(log *slog.Logger, cfg *config.Config, st *settings.Store) *Dynamic {
	return &Dynamic{log: log, cfg: cfg, settings: st}
}

func (d *Dynamic) provider() string {
	if d.settings != nil && d.settings.Configured("mail.provider") {
		return d.settings.GetString("mail.provider")
	}
	if d.cfg != nil && d.cfg.Mail.Enabled {
		return "smtp"
	}
	return "log"
}

func (d *Dynamic) value(key, fallback string) string {
	if d.settings != nil {
		if v := d.settings.GetString(key); v != "" {
			return v
		}
	}
	return fallback
}

func (d *Dynamic) Send(ctx context.Context, msg Message) error {
	if len(msg.To) == 0 {
		return fmt.Errorf("mail: no recipients")
	}
	switch d.provider() {
	case "resend":
		key := d.value("mail.resend.api_key", os.Getenv("RESEND_API_KEY"))
		from := d.value("mail.from", d.cfgMailFrom())
		base := d.value("mail.resend.base_url", "https://api.resend.com")
		return NewResend(base, key, from).Send(ctx, msg)
	case "smtp":
		host := d.value("mail.smtp.host", d.cfgMailHost())
		port := d.cfgMailPort()
		if d.settings != nil && d.settings.Configured("mail.smtp.port") {
			port = int(d.settings.GetInt("mail.smtp.port"))
		}
		user := d.value("mail.smtp.username", d.cfgMailUser())
		pass := d.value("mail.smtp.password", d.cfgMailPass())
		from := d.value("mail.from", d.cfgMailFrom())
		return NewSMTP(host, port, user, pass, from).Send(ctx, msg)
	default:
		return NewLogMailer(d.log).Send(ctx, msg)
	}
}

func (d *Dynamic) cfgMailFrom() string {
	if d.cfg != nil {
		return d.cfg.Mail.From
	}
	return ""
}
func (d *Dynamic) cfgMailHost() string {
	if d.cfg != nil {
		return d.cfg.Mail.SMTPHost
	}
	return ""
}
func (d *Dynamic) cfgMailPort() int {
	if d.cfg != nil && d.cfg.Mail.SMTPPort > 0 {
		return d.cfg.Mail.SMTPPort
	}
	return 587
}
func (d *Dynamic) cfgMailUser() string {
	if d.cfg != nil {
		return d.cfg.Mail.Username
	}
	return ""
}
func (d *Dynamic) cfgMailPass() string {
	if d.cfg != nil {
		return d.cfg.Mail.Password
	}
	return ""
}

// SMTPMailer sends mail through an SMTP relay, with implicit TLS on port 465.
type SMTPMailer struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

func NewSMTP(host string, port int, username, password, from string) *SMTPMailer {
	return &SMTPMailer{Host: host, Port: port, Username: username, Password: password, From: from}
}

func (m *SMTPMailer) Send(ctx context.Context, msg Message) error {
	if m.Host == "" {
		return fmt.Errorf("mail: smtp host is empty")
	}
	if m.From == "" {
		return fmt.Errorf("mail: from address is empty")
	}
	body := buildMIME(m.From, msg)
	addr := net.JoinHostPort(m.Host, strconv.Itoa(m.Port))
	var auth smtp.Auth
	if m.Username != "" {
		auth = smtp.PlainAuth("", m.Username, m.Password, m.Host)
	}

	// Every SMTP step gets a deadline so a silent server cannot leave the
	// goroutine (and its socket) hanging after the caller has given up.
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	done := make(chan error, 1)
	go func() {
		if m.Port == 465 {
			done <- m.sendImplicitTLS(addr, auth, msg.To, body, deadline)
			return
		}
		done <- m.sendStartTLS(addr, auth, msg.To, body, deadline)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		return err
	}
}

// sendStartTLS delivers over the standard STARTTLS path with a hard deadline on
// the connection, so a stalled server cannot block the goroutine forever.
func (m *SMTPMailer) sendStartTLS(addr string, auth smtp.Auth, to []string, body []byte, deadline time.Time) error {
	dialer := &net.Dialer{Timeout: 15 * time.Second, Deadline: deadline}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(deadline)
	client, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: m.Host}); err != nil {
			return err
		}
	}
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(m.From); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func (m *SMTPMailer) sendImplicitTLS(addr string, auth smtp.Auth, to []string, body []byte, deadline time.Time) error {
	dialer := &net.Dialer{Timeout: 15 * time.Second, Deadline: deadline}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: m.Host})
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(deadline)
	client, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer client.Close()
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(m.From); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func buildMIME(from string, msg Message) []byte {
	var b bytes.Buffer
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(msg.To, ", ") + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", msg.Subject) + "\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	if msg.HTML != "" {
		b.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
		b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
		b.WriteString(msg.HTML)
	} else {
		b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
		b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
		b.WriteString(msg.Text)
	}
	b.WriteString("\r\n")
	return b.Bytes()
}

// ResendMailer sends transactional email through the Resend HTTP API.
type ResendMailer struct {
	BaseURL string
	APIKey  string
	From    string
	Client  *http.Client
}

func NewResend(baseURL, apiKey, from string) *ResendMailer {
	return &ResendMailer{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		From:    from,
		Client:  &http.Client{Timeout: 20 * time.Second},
	}
}

func (m *ResendMailer) Send(ctx context.Context, msg Message) error {
	if m.APIKey == "" {
		return fmt.Errorf("mail: resend api key is empty")
	}
	if m.From == "" {
		return fmt.Errorf("mail: from address is empty")
	}
	htmlBody := msg.HTML
	if htmlBody == "" && msg.Text != "" {
		htmlBody = "<div style=\"white-space:pre-wrap;font-family:Arial,sans-serif\">" + html.EscapeString(msg.Text) + "</div>"
	}
	payload := map[string]any{
		"from":    m.From,
		"to":      msg.To,
		"subject": msg.Subject,
	}
	if htmlBody != "" {
		payload["html"] = htmlBody
	}
	if msg.Text != "" {
		payload["text"] = msg.Text
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.BaseURL+"/emails", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("resend: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
