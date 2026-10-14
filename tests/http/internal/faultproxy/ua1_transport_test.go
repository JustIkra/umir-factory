package faultproxy

import (
	"context"
	"crypto/tls"
	"net"
	"net/smtp"
	"testing"
	"time"
)

func TestUA1SMTPActualTLSCapturePermanentAndLostReply(t *testing.T) {
	for _, mode := range []string{"ack", "permanent-reject", "accept-drop-reply"} {
		t.Run(mode, func(t *testing.T) {
			receiver, identity := newUA1Receiver(t)
			if err := receiver.BindDiscovery(identity, ua1Discovery(t, identity)); err != nil {
				t.Fatal(err)
			}
			selector := UA1LinkSelector{JobID: fixtureJob, InvitationID: fixtureChallenge}
			if mode != "ack" {
				if err := receiver.ArmFault(ua1Org, selector, mode, 30*time.Second); err != nil {
					t.Fatal(err)
				}
			}
			cert, roots := sinkTestTLS(t)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- ServeUA1SMTP(ctx, listener, receiver, cert, "umir-e2e", "synthetic-password") }()
			t.Cleanup(func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			})
			connection, err := net.DialTimeout("tcp", listener.Addr().String(), 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			client, err := smtp.NewClient(connection, "smtp")
			if err != nil {
				if closeErr := connection.Close(); closeErr != nil {
					t.Error(closeErr)
				}
				t.Fatal(err)
			}
			defer func() {
				if err := client.Close(); err != nil {
					t.Error(err)
				}
			}()
			if err := client.StartTLS(&tls.Config{RootCAs: roots, ServerName: "smtp", MinVersion: tls.VersionTLS12}); err != nil {
				t.Fatal(err)
			}
			if err := client.Auth(smtp.PlainAuth("", "umir-e2e", "synthetic-password", "smtp")); err != nil {
				t.Fatal(err)
			}
			if err := client.Mail("sender@umir.invalid"); err != nil {
				t.Fatal(err)
			}
			if err := client.Rcpt("recipient@umir.invalid"); err != nil {
				t.Fatal(err)
			}
			data, err := client.Data()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := data.Write(ua1RawLink()); err != nil {
				t.Fatal(err)
			}
			closeErr := data.Close()
			if (closeErr == nil) != (mode == "ack") {
				t.Fatal("actual SMTP acknowledgment disagreed with selected fault")
			}
			observed, err := receiver.Captures(selector, 1, 64)
			if err != nil || len(observed.Captures) != 1 || observed.Captures[0].Accepted != (mode != "permanent-reject") {
				t.Fatal("actual SMTP receipt fact was invented")
			}
		})
	}
}
