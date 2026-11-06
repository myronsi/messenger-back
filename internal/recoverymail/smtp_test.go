package recoverymail

import (
	"context"
	"fmt"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/myronsi/messenger-back/internal/config"
)

// This server never leaves loopback and deliberately has no STARTTLS capability.
func localMailServer(t *testing.T) (config.RecoveryMail, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	messages := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		protocol := textproto.NewConn(conn)
		_ = protocol.PrintfLine("220 localhost test")
		for {
			line, err := protocol.ReadLine()
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
				_ = protocol.PrintfLine("250 localhost")
			case strings.HasPrefix(line, "MAIL FROM:"), strings.HasPrefix(line, "RCPT TO:"):
				_ = protocol.PrintfLine("250 accepted")
			case line == "DATA":
				_ = protocol.PrintfLine("354 enter message")
				data, err := protocol.ReadDotBytes()
				if err != nil {
					return
				}
				messages <- string(data)
				_ = protocol.PrintfLine("250 queued")
			case line == "QUIT":
				_ = protocol.PrintfLine("221 bye")
				return
			default:
				_ = protocol.PrintfLine("500 unsupported")
			}
		}
	}()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	n, _ := strconv.Atoi(port)
	return config.RecoveryMail{Host: "127.0.0.1", Port: n, From: "sender@example.test", TLSMode: "plain"}, messages
}

func TestLocalSMTPDelivery(t *testing.T) {
	cfg, messages := localMailServer(t)
	if err := (Sender{cfg}).Send(context.Background(), "recipient@example.test", "Verify your email", "First line\n.Secret link"); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-messages:
		for _, want := range []string{"From: sender@example.test", "To: recipient@example.test", "Subject: Verify your email", "charset=utf-8", "First line\n.Secret link"} {
			if !strings.Contains(message, want) {
				t.Errorf("message missing %q", want)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("no message delivered")
	}
}

func TestSMTPRequiresAdvertisedSTARTTLS(t *testing.T) {
	cfg, messages := localMailServer(t)
	cfg.TLSMode = "starttls"
	err := (Sender{cfg}).Send(context.Background(), "recipient@example.test", "Verify", "Secret link")
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("error = %v", err)
	}
	select {
	case <-messages:
		t.Fatal("sent without TLS")
	default:
	}
}

func TestSMTPRejectsInjectedHeaders(t *testing.T) {
	for _, input := range []struct{ to, subject string }{
		{"recipient@example.test\r\nBcc: other@example.test", "Verify"},
		{"recipient@example.test", "Verify\nBcc: other@example.test"},
		{"not an address", "Verify"},
	} {
		t.Run(fmt.Sprintf("%q/%q", input.to, input.subject), func(t *testing.T) {
			if err := (Sender{}).Send(context.Background(), input.to, input.subject, "body"); err == nil {
				t.Fatal("accepted invalid header")
			}
		})
	}
}
