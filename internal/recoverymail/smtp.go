package recoverymail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/myronsi/messenger-back/internal/config"
)

type Sender struct{ Config config.RecoveryMail }

func (s Sender) Send(ctx context.Context, to, subject, body string) error {
	if strings.ContainsAny(to+subject, "\r\n") {
		return errors.New("invalid mail header")
	}
	parsed, err := mail.ParseAddress(to)
	if err != nil || parsed.Address != to {
		return errors.New("invalid recipient")
	}
	address := net.JoinHostPort(s.Config.Host, strconv.Itoa(s.Config.Port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	tlsConfig := &tls.Config{ServerName: s.Config.Host, MinVersion: tls.VersionTLS12}
	var conn net.Conn
	if s.Config.TLSMode == "tls" {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err = conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	client, err := smtp.NewClient(conn, s.Config.Host)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	if s.Config.TLSMode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("mail server requires STARTTLS support")
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if s.Config.Username != "" {
		if err = client.Auth(smtp.PlainAuth("", s.Config.Username, s.Config.Password.Reveal(), s.Config.Host)); err != nil {
			return err
		}
	}
	if err = client.Mail(s.Config.From); err != nil {
		return err
	}
	if err = client.Rcpt(to); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	message := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s", s.Config.From, to, subject, time.Now().Format(time.RFC1123Z), strings.ReplaceAll(body, "\n", "\r\n"))
	if _, err = writer.Write([]byte(message)); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}
