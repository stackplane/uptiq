package alerting

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"sitelert/internal/config"
)

// Email configuration constants.
const (
	emailDialTimeout  = 7 * time.Second
	emailImplicitPort = 465
	emailMinTLS       = tls.VersionTLS12
)

// sendEmail sends an email alert using SMTP.
func sendEmail(ctx context.Context, ch config.Channel, subject, body string) error {
	if err := validateEmailConfig(ch); err != nil {
		return err
	}

	fromAddr, err := parseEmailAddress(ch.From)
	if err != nil {
		return fmt.Errorf("parse from: %w", err)
	}

	toAddrs, toHeaders, err := parseRecipients(ch.To)
	if err != nil {
		return err
	}

	msg := buildEmailMessage(ch.From, toHeaders, subject, body)

	return sendSMTP(ctx, ch, fromAddr, toAddrs, msg)
}

func validateEmailConfig(ch config.Channel) error {
	if strings.TrimSpace(ch.SMTPHost) == "" {
		return fmt.Errorf("smtp_host is empty")
	}
	if ch.SMTPPort == 0 {
		return fmt.Errorf("smtp_port is empty/0")
	}
	if strings.TrimSpace(ch.From) == "" {
		return fmt.Errorf("from is empty")
	}
	if len(ch.To) == 0 {
		return fmt.Errorf("to list is empty")
	}
	return nil
}

func parseEmailAddress(s string) (string, error) {
	addr, err := mail.ParseAddress(strings.TrimSpace(s))
	if err != nil {
		return "", err
	}
	return addr.Address, nil
}

func parseRecipients(recipients []string) (addrs, headers []string, err error) {
	for _, r := range recipients {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}

		addr, err := parseEmailAddress(r)
		if err != nil {
			return nil, nil, fmt.Errorf("parse to %q: %w", r, err)
		}

		addrs = append(addrs, addr)
		headers = append(headers, r)
	}

	if len(addrs) == 0 {
		return nil, nil, fmt.Errorf("no valid recipients in to list")
	}

	return addrs, headers, nil
}

func sendSMTP(ctx context.Context, ch config.Channel, fromAddr string, toAddrs []string, msg []byte) error {
	addr := net.JoinHostPort(ch.SMTPHost, fmt.Sprintf("%d", ch.SMTPPort))
	conn, err := dialWithContext(ctx, addr)
	if err != nil {
		return fmt.Errorf("dial smtp: %w", err)
	}
	defer conn.Close()

	implicitTLS := ch.SMTPPort == emailImplicitPort
	if implicitTLS {
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName: ch.SMTPHost,
			MinVersion: emailMinTLS,
		})
		if err := tlsConn.Handshake(); err != nil {
			return fmt.Errorf("tls handshake: %w", err)
		}
		conn = tlsConn
	}

	client, err := smtp.NewClient(conn, ch.SMTPHost)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer client.Close()

	isTLS := implicitTLS
	if !implicitTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{
				ServerName: ch.SMTPHost,
				MinVersion: emailMinTLS,
			}); err != nil {
				return fmt.Errorf("starttls: %w", err)
			}
			isTLS = true
		}
	}

	// Refuse to send credentials without TLS
	authConfigured := strings.TrimSpace(ch.Username) != "" || strings.TrimSpace(ch.Password) != ""
	if authConfigured && !isTLS {
		return fmt.Errorf("refusing to authenticate without TLS (enable STARTTLS or use port 465)")
	}

	if strings.TrimSpace(ch.Username) != "" {
		auth := smtp.PlainAuth("", ch.Username, ch.Password, ch.SMTPHost)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := client.Mail(fromAddr); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}

	for _, rcpt := range toAddrs {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("rcpt to %s: %w", rcpt, err)
		}
	}

	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}

	if _, err := writer.Write(msg); err != nil {
		writer.Close()
		return fmt.Errorf("write data: %w", err)
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("close data: %w", err)
	}

	_ = client.Quit()
	return nil
}

func dialWithContext(ctx context.Context, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: emailDialTimeout}

	connCh := make(chan net.Conn, 1)
	errCh := make(chan error, 1)

	go func() {
		conn, err := dialer.Dial("tcp", addr)
		if err != nil {
			errCh <- err
			return
		}
		connCh <- conn
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-errCh:
		return nil, err
	case conn := <-connCh:
		return conn, nil
	}
}

func buildEmailMessage(from string, to []string, subject, body string) []byte {
	var buf bytes.Buffer

	writeHeader(&buf, "From", from)
	writeHeader(&buf, "To", strings.Join(to, ", "))
	writeHeader(&buf, "Subject", sanitizeHeader(subject))
	writeHeader(&buf, "Date", time.Now().Format(time.RFC1123Z))
	writeHeader(&buf, "MIME-Version", "1.0")
	writeHeader(&buf, "Content-Type", `text/plain; charset="utf-8"`)
	writeHeader(&buf, "Content-Transfer-Encoding", "8bit")
	buf.WriteString("\r\n")

	// Normalize line endings to CRLF
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	body = strings.ReplaceAll(body, "\n", "\r\n")
	buf.WriteString(body)

	if !strings.HasSuffix(body, "\r\n") {
		buf.WriteString("\r\n")
	}

	return buf.Bytes()
}

func writeHeader(buf *bytes.Buffer, key, value string) {
	buf.WriteString(key)
	buf.WriteString(": ")
	buf.WriteString(value)
	buf.WriteString("\r\n")
}

func sanitizeHeader(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}
