package faultproxy

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSyntheticSMTPUsesVerifiedTLSAndRejectsRealRecipient(t *testing.T) {
	s := newSinkForTest(t, "smtp")
	cert, roots := sinkTestTLS(t)
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeFixtureSMTP(ctx, listener, s, cert, "umir-e2e", "synthetic-password") }()
	t.Cleanup(func() {
		cancel()
		if e := <-done; e != nil {
			t.Error(e)
		}
	})
	conn, e := net.Dial("tcp", listener.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	c, e := smtp.NewClient(conn, "smtp")
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if e := c.Close(); e != nil {
			t.Error(e)
		}
	}()
	if e := c.StartTLS(&tls.Config{RootCAs: roots, ServerName: "smtp", MinVersion: tls.VersionTLS12}); e != nil {
		t.Fatal(e)
	}
	if e := c.Auth(smtp.PlainAuth("", "umir-e2e", "synthetic-password", "smtp")); e != nil {
		t.Fatal(e)
	}
	if e := c.Mail("sender@umir.invalid"); e != nil {
		t.Fatal(e)
	}
	if e := c.Rcpt("recipient@example.com"); e == nil {
		t.Fatal("accepted real destination")
	}
	if e := c.Rcpt("recipient@umir.invalid"); e != nil {
		t.Fatal(e)
	}
	w, e := c.Data()
	if e != nil {
		t.Fatal(e)
	}
	raw := "From: sender@umir.invalid\r\nTo: recipient@umir.invalid\r\nContent-Type: text/plain; charset=utf-8\r\nX-Umir-Fixture-Job-ID: " + fixtureJob + "\r\nX-Umir-Fixture-Challenge-ID: " + fixtureChallenge + "\r\n\r\nVerification code:\r\n" + "654321" + "\r\n"
	if _, e := w.Write([]byte(raw)); e != nil {
		t.Fatal(e)
	}
	if e := w.Close(); e != nil {
		t.Fatal(e)
	}
	got, e := s.Operation(ctx, "sink.capture", map[string]any{"afterSequence": "1", "selector": map[string]any{"kind": "auth-otp", "jobID": fixtureJob, "challengeID": fixtureChallenge}, "limit": 1, "waitMs": 0})
	if e != nil {
		t.Fatal(e)
	}
	caps, ok := got["captures"].([]any)
	if !ok || len(caps) != 1 {
		t.Fatal("real SMTP did not capture")
	}
}
func sinkTestTLS(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	template := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic"}, DNSNames: []string{"smtp", "api.telegram.org"}, NotBefore: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), NotAfter: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, e := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	priv, e := x509.MarshalECPrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	cert, e := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: priv}))
	if e != nil {
		t.Fatal(e)
	}
	parsed, e := x509.ParseCertificate(der)
	if e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return cert, roots
}

func TestNotificationCONNECTStaysLocalAndChecksPrivacy(t *testing.T) {
	s := newSinkForTest(t, "notification")
	cert, roots := sinkTestTLS(t)
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	bot := "1:" + strings.Repeat("a", 32)
	go func() { done <- ServeFixtureNotification(ctx, listener, s, cert, "umir-e2e", "proxy-password", bot) }()
	t.Cleanup(func() {
		cancel()
		if e := <-done; e != nil {
			t.Error(e)
		}
	})
	proxy, e := url.Parse("http://" + listener.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	proxy.User = url.UserPassword("umir-e2e", "proxy-password")
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	text := "Новое обращение\nID: " + fixtureChallenge + "\nВремя: 2026-10-13T09:00:00+03:00\nПроверьте служебную почту."
	for _, extra := range []bool{false, true} {
		payload := map[string]any{"chat_id": "1", "text": text}
		if extra {
			payload["email"] = "private@umir.invalid"
		}
		raw, e := json.Marshal(payload)
		if e != nil {
			t.Fatal(e)
		}
		req, e := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+bot+"/sendMessage", bytes.NewReader(raw))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("X-Umir-Fixture-Job-ID", fixtureJob)
		req.Header.Set("X-Umir-Fixture-Submission-ID", fixtureChallenge)
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		if e := res.Body.Close(); e != nil {
			t.Fatal(e)
		}
		want := 200
		if extra {
			want = 400
		}
		if res.StatusCode != want {
			t.Fatalf("privacy status %d want %d", res.StatusCode, want)
		}
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com", nil)
	if e != nil {
		t.Fatal(e)
	}
	res, e := client.Do(req)
	if e == nil {
		if e := res.Body.Close(); e != nil {
			t.Fatal(e)
		}
		t.Fatal("foreign CONNECT forwarded")
	}
}
