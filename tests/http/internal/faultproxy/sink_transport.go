package faultproxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"

	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"time"
)

func serveFixtureConnections(ctx context.Context, l net.Listener, handle func(net.Conn)) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	active := map[net.Conn]bool{}
	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			if e := l.Close(); e != nil && !errors.Is(e, net.ErrClosed) {
				return
			}
			mu.Lock()
			for conn := range active {
				if e := conn.Close(); e != nil && !errors.Is(e, net.ErrClosed) {
					continue
				}
			}
			mu.Unlock()
		case <-stop:
		}
	}()
	defer func() { close(stop); <-stopped; wg.Wait() }()
	slots := make(chan struct{}, 8)
	for {
		conn, e := l.Accept()
		if e != nil {
			if ctx.Err() != nil || errors.Is(e, net.ErrClosed) {
				return nil
			}
			return e
		}
		select {
		case slots <- struct{}{}:
		default:
			if e := conn.Close(); e != nil {
				return e
			}
			continue
		}
		mu.Lock()
		active[conn] = true
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				mu.Lock()
				delete(active, conn)
				mu.Unlock()
				<-slots
				if e := conn.Close(); e != nil && !errors.Is(e, net.ErrClosed) {
					return
				}
			}()
			if e := conn.SetDeadline(time.Now().Add(10 * time.Second)); e != nil {
				return
			}
			handle(conn)
		}()
	}
}
func smtpLine(r *bufio.Reader, limit int) (string, error) {
	b, e := r.ReadSlice('\n')
	if e != nil || len(b) > limit {
		return "", ErrProtocol
	}
	return strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r"), nil
}
func smtpReply(w *bufio.Writer, line string) error {
	if _, e := w.WriteString(line + "\r\n"); e != nil {
		return e
	}
	return w.Flush()
}
func secureEqual(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func syntheticAddress(s string) bool {
	a, e := mail.ParseAddress(s)
	if e != nil {
		return false
	}
	at := strings.LastIndexByte(a.Address, '@')
	return at > 0 && strings.HasSuffix(strings.ToLower(a.Address[at+1:]), ".invalid")
}
func smtpAddressArg(arg string) (string, error) {
	start := strings.Index(arg, "<")
	end := strings.Index(arg, ">")
	if start < 0 || end <= start || strings.TrimSpace(arg[end+1:]) != "" {
		return "", ErrProtocol
	}
	a := arg[start+1 : end]
	if !syntheticAddress(a) {
		return "", ErrProtocol
	}
	return a, nil
}
func ServeFixtureSMTP(ctx context.Context, l net.Listener, s *FixtureSink, cert tls.Certificate, user, password string) error {
	return serveFixtureConnections(ctx, l, func(conn net.Conn) {
		r := bufio.NewReaderSize(conn, 65538)
		w := bufio.NewWriter(conn)
		if smtpReply(w, "220 smtp synthetic ESMTP") != nil {
			return
		}
		tlsActive, authed, mailSet := false, false, false
		recipients := 0
		for {
			line, e := smtpLine(r, 512)
			if e != nil {
				return
			}
			verb, arg, _ := strings.Cut(line, " ")
			switch strings.ToUpper(verb) {
			case "EHLO":
				response := "250-smtp\r\n250-SIZE 65536\r\n250-STARTTLS\r\n250 AUTH PLAIN LOGIN"
				if smtpReply(w, response) != nil {
					return
				}
			case "HELO":
				if smtpReply(w, "250 smtp") != nil {
					return
				}
			case "STARTTLS":
				if tlsActive || arg != "" {
					if smtpReply(w, "503 bad sequence") != nil {
						return
					}
					continue
				}
				if smtpReply(w, "220 ready for TLS") != nil {
					return
				}
				tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
				if e := tlsConn.HandshakeContext(ctx); e != nil {
					return
				}
				conn = tlsConn
				r = bufio.NewReaderSize(conn, 65538)
				w = bufio.NewWriter(conn)
				tlsActive = true
				authed = false
				mailSet = false
				recipients = 0
			case "AUTH":
				if !tlsActive {
					if smtpReply(w, "538 encryption required") != nil {
						return
					}
					continue
				}
				mechanism, encoded, _ := strings.Cut(arg, " ")
				valid := false
				switch strings.ToUpper(mechanism) {
				case "PLAIN":
					if encoded == "" {
						if smtpReply(w, "334 ") != nil {
							return
						}
						encoded, e = smtpLine(r, 512)
						if e != nil {
							return
						}
					}
					decoded, e := base64.StdEncoding.DecodeString(encoded)
					parts := bytes.Split(decoded, []byte{0})
					valid = e == nil && len(parts) == 3 && len(parts[0]) == 0 && secureEqual(string(parts[1]), user) && secureEqual(string(parts[2]), password)
				case "LOGIN":
					if encoded == "" {
						if smtpReply(w, "334 VXNlcm5hbWU6") != nil {
							return
						}
						encoded, e = smtpLine(r, 512)
						if e != nil {
							return
						}
					}
					username, ue := base64.StdEncoding.DecodeString(encoded)
					if smtpReply(w, "334 UGFzc3dvcmQ6") != nil {
						return
					}
					encoded, e = smtpLine(r, 512)
					if e != nil {
						return
					}
					pass, pe := base64.StdEncoding.DecodeString(encoded)
					valid = ue == nil && pe == nil && secureEqual(string(username), user) && secureEqual(string(pass), password)
				}
				authed = valid
				if valid {
					e = smtpReply(w, "235 authenticated")
				} else {
					e = smtpReply(w, "535 authentication failed")
				}
				if e != nil {
					return
				}
			case "MAIL":
				if !authed || !strings.HasPrefix(strings.ToUpper(arg), "FROM:") {
					if smtpReply(w, "530 authentication required") != nil {
						return
					}
					continue
				}
				if _, e := smtpAddressArg(arg[5:]); e != nil {
					if smtpReply(w, "550 synthetic sender required") != nil {
						return
					}
					continue
				}
				mailSet = true
				recipients = 0
				if smtpReply(w, "250 sender accepted") != nil {
					return
				}
			case "RCPT":
				if !mailSet || !strings.HasPrefix(strings.ToUpper(arg), "TO:") {
					if smtpReply(w, "503 bad sequence") != nil {
						return
					}
					continue
				}
				if _, e := smtpAddressArg(arg[3:]); e != nil || recipients >= 8 {
					if smtpReply(w, "550 synthetic recipient required") != nil {
						return
					}
					continue
				}
				recipients++
				if smtpReply(w, "250 recipient accepted") != nil {
					return
				}
			case "DATA":
				if !mailSet || recipients == 0 || arg != "" {
					if smtpReply(w, "503 bad sequence") != nil {
						return
					}
					continue
				}
				if smtpReply(w, "354 end with dot") != nil {
					return
				}
				var raw bytes.Buffer
				for {
					line, e := smtpLine(r, 65536)
					if e != nil {
						return
					}
					if line == "." {
						break
					}
					if strings.HasPrefix(line, "..") {
						line = line[1:]
					}
					if raw.Len()+len(line)+2 > 65536 {
						if smtpReply(w, "552 message too large") != nil {
							return
						}
						return
					}
					if _, e := raw.WriteString(line + "\r\n"); e != nil {
						return
					}
				}
				c, synthetic, e := smtpCorrelation(raw.Bytes())
				if e != nil {
					if smtpReply(w, "550 invalid synthetic message") != nil {
						return
					}
					continue
				}
				reply, e := s.Accept(ctx, c, raw.Bytes(), synthetic, true)
				mailSet = false
				recipients = 0
				if e != nil {
					if smtpReply(w, "550 synthetic policy rejected") != nil {
						return
					}
					continue
				}
				switch reply {
				case "lost":
					return
				case "temporary":
					e = smtpReply(w, "451 temporary synthetic rejection")
				case "permanent":
					e = smtpReply(w, "550 permanent synthetic rejection")
				default:
					e = smtpReply(w, "250 captured")
				}
				if e != nil {
					return
				}
			case "RSET":
				mailSet = false
				recipients = 0
				if smtpReply(w, "250 reset") != nil {
					return
				}
			case "NOOP":
				if smtpReply(w, "250 ok") != nil {
					return
				}
			case "QUIT":
				if smtpReply(w, "221 goodbye") != nil {
					return
				}
				return
			default:
				if smtpReply(w, "500 unsupported command") != nil {
					return
				}
			}
		}
	})
}
func smtpCorrelation(raw []byte) (map[string]any, bool, error) {
	m, e := mail.ReadMessage(bytes.NewReader(raw))
	if e != nil {
		return nil, false, ErrProtocol
	}
	for _, name := range []string{"From", "To", "Cc", "Bcc", "Reply-To"} {
		values := m.Header[name]
		for _, v := range values {
			addresses, e := mail.ParseAddressList(v)
			if e != nil {
				return nil, false, ErrProtocol
			}
			for _, a := range addresses {
				if !syntheticAddress(a.Address) {
					return nil, false, ErrProtocol
				}
			}
		}
	}
	headers := http.Header(m.Header)
	c, e := transportCorrelation(headers, "smtp")
	return c, true, e
}
func transportCorrelation(h http.Header, channel string) (map[string]any, error) {
	for _, n := range []string{"X-Umir-Fixture-Job-ID", "X-Umir-Fixture-Challenge-ID", "X-Umir-Fixture-Submission-ID"} {
		if len(h.Values(n)) > 1 {
			return nil, ErrProtocol
		}
	}
	job := h.Get("X-Umir-Fixture-Job-ID")
	challenge := h.Get("X-Umir-Fixture-Challenge-ID")
	submission := h.Get("X-Umir-Fixture-Submission-ID")
	var c map[string]any
	if challenge != "" && submission == "" {
		c = map[string]any{"kind": "auth-otp", "jobID": job, "challengeID": challenge}
	} else if submission != "" && challenge == "" {
		c = map[string]any{"kind": "feedback", "jobID": job, "submissionID": submission, "channel": channel}
	} else {
		return nil, ErrProtocol
	}
	if ValidateOperational("#/$defs/Correlation", canonical(c)) != nil {
		return nil, ErrProtocol
	}
	return c, nil
}
func ServeFixtureNotification(ctx context.Context, l net.Listener, s *FixtureSink, cert tls.Certificate, user, password, bot string) error {
	return serveFixtureConnections(ctx, l, func(conn net.Conn) {
		reader := bufio.NewReaderSize(io.LimitReader(conn, 16384), 8192)
		req, e := http.ReadRequest(reader)
		if e != nil {
			return
		}
		if e := req.Body.Close(); e != nil {
			return
		}
		auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))
		if req.Method != http.MethodConnect || req.Host != "api.telegram.org:443" || req.RequestURI != "api.telegram.org:443" || !secureEqual(req.Header.Get("Proxy-Authorization"), auth) {
			if _, e := io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"); e != nil {
				return
			}
			return
		}
		if _, e := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); e != nil {
			return
		}
		tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
		if e := tlsConn.HandshakeContext(ctx); e != nil {
			return
		}
		req, e = http.ReadRequest(bufio.NewReaderSize(io.LimitReader(tlsConn, 81920), 8192))
		if e != nil {
			return
		}
		defer func() {
			if e := req.Body.Close(); e != nil {
				return
			}
		}()
		if req.Method != http.MethodPost || req.Host != "api.telegram.org" || req.URL.Path != "/bot"+bot+"/sendMessage" || req.URL.RawQuery != "" {
			writeTelegram(tlsConn, 400)
			return
		}
		raw, e := io.ReadAll(io.LimitReader(req.Body, 65537))
		if e != nil || len(raw) > 65536 {
			writeTelegram(tlsConn, 400)
			return
		}
		v, e := StrictJSON(raw, 4)
		body, ok := v.(map[string]any)
		if e != nil || !ok || len(body) != 2 || body["chat_id"] != "1" {
			writeTelegram(tlsConn, 400)
			return
		}
		c, e := transportCorrelation(req.Header, "telegram")
		if e != nil {
			writeTelegram(tlsConn, 400)
			return
		}
		text, ok := body["text"].(string)
		safe := ok && notificationSafe(text, c)
		if !safe {
			writeTelegram(tlsConn, 400)
			return
		}
		reply, e := s.Accept(ctx, c, raw, true, safe)
		if e != nil {
			writeTelegram(tlsConn, 400)
			return
		}
		switch reply {
		case "lost":
			return
		case "temporary":
			writeTelegram(tlsConn, 503)
		case "permanent":
			writeTelegram(tlsConn, 400)
		default:
			writeTelegram(tlsConn, 200)
		}
	})
}
func notificationSafe(text string, c map[string]any) bool {
	id, ok := c["submissionID"].(string)
	if !ok {
		return false
	}
	prefix := "Новое обращение\nID: " + id + "\nВремя: "
	suffix := "\nПроверьте служебную почту."
	if !strings.HasPrefix(text, prefix) || !strings.HasSuffix(text, suffix) {
		return false
	}
	instant := strings.TrimSuffix(strings.TrimPrefix(text, prefix), suffix)
	return validInstant(instant) && !regexp.MustCompile(`[@<>]`).MatchString(instant)
}
func writeTelegram(w io.Writer, status int) {
	body := `{"ok":true,"result":{"message_id":1}}`
	if status != 200 {
		body = fmt.Sprintf(`{"ok":false,"error_code":%d}`, status)
	}
	if _, e := fmt.Fprintf(w, "HTTP/1.1 %d Result\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", status, len(body), body); e != nil {
		return
	}
}
