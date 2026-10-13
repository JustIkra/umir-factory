package faultproxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"syscall"
	"time"
)

type FixtureFileRef struct {
	Target string `json:"target"`
	Path   string `json:"path"`
}
type SinkConfig struct {
	Version               int                       `json:"version"`
	Protocol              string                    `json:"protocol"`
	Project               string                    `json:"project"`
	Target                string                    `json:"target"`
	ControlPort           int                       `json:"controlPort"`
	DeliveryPort          int                       `json:"deliveryPort"`
	ControlCredentialFile FixtureFileRef            `json:"controlCredentialFile"`
	CredentialFiles       map[string]FixtureFileRef `json:"credentialFiles"`
	CaptureLimit          int                       `json:"captureLimit"`
	MessageBytes          int                       `json:"messageBytes"`
	TotalCaptureBytes     int                       `json:"totalCaptureBytes"`
}

func ReadPrivateFile(path string, limit int) ([]byte, error) {
	if !filepath.IsAbs(path) || limit < 1 {
		return nil, ErrProtocol
	}
	for p := filepath.Dir(path); p != "/"; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrProtocol
		}
	}
	fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, ErrProtocol
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		if e := syscall.Close(fd); e != nil {
			return nil, ErrProtocol
		}
		return nil, ErrProtocol
	}
	info, e := f.Stat()
	if e != nil {
		closeErr := f.Close()
		return nil, errors.Join(ErrProtocol, closeErr)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || st.Uid != uint32(os.Geteuid()) || st.Gid != uint32(os.Getegid()) || info.Size() > int64(limit) {
		closeErr := f.Close()
		return nil, errors.Join(ErrProtocol, closeErr)
	}
	b, e := io.ReadAll(io.LimitReader(f, int64(limit+1)))
	e = errors.Join(e, f.Close())
	if e != nil || len(b) > limit {
		return nil, ErrProtocol
	}
	return b, nil
}
func readSinkRef(ref FixtureFileRef, target, name string, limit int) ([]byte, error) {
	if ref.Target != target || ref.Path != "/run/umir-fixtures/secrets/"+name {
		return nil, ErrProtocol
	}
	return ReadPrivateFile(ref.Path, limit)
}
func loadSinkConfig(role string) (SinkConfig, []byte, map[string][]byte, error) {
	raw, e := ReadPrivateFile("/run/umir-fixtures/config/config.json", 16384)
	if e != nil {
		return SinkConfig{}, nil, nil, ErrProtocol
	}
	if ValidateOperational("#/$defs/SinkConfig", raw) != nil {
		return SinkConfig{}, nil, nil, ErrProtocol
	}
	var cfg SinkConfig
	if json.Unmarshal(raw, &cfg) != nil || cfg.Target != role {
		return SinkConfig{}, nil, nil, ErrProtocol
	}
	names := []string{"synthetic-ca.pem"}
	if role == "smtp" {
		names = append(names, "smtp-username", "smtp-password", "smtp-tls-cert", "smtp-tls-key")
	} else {
		names = append(names, "proxy-username", "proxy-password", "telegram-bot", "notification-tls-cert", "notification-tls-key")
	}
	if len(cfg.CredentialFiles) != len(names) {
		return SinkConfig{}, nil, nil, ErrProtocol
	}
	keys := map[string][]byte{}
	for _, name := range names {
		ref, ok := cfg.CredentialFiles[name]
		if !ok {
			return SinkConfig{}, nil, nil, ErrProtocol
		}
		keys[name], e = readSinkRef(ref, role, name, 16384)
		if e != nil || len(keys[name]) == 0 {
			return SinkConfig{}, nil, nil, ErrProtocol
		}
	}
	keys["control"], e = readSinkRef(cfg.ControlCredentialFile, role, "fixture-"+role, 64)
	if e != nil || !regexp.MustCompile(`^[0-9a-f]{64}$`).Match(keys["control"]) {
		return SinkConfig{}, nil, nil, ErrProtocol
	}
	for _, name := range []string{"smtp-password", "proxy-password"} {
		if val, ok := keys[name]; ok && !regexp.MustCompile(`^[0-9a-f]{32}$`).Match(val) {
			return SinkConfig{}, nil, nil, ErrProtocol
		}
	}
	for _, name := range []string{"smtp-username", "proxy-username"} {
		if val, ok := keys[name]; ok && string(val) != "umir-e2e" {
			return SinkConfig{}, nil, nil, ErrProtocol
		}
	}
	if val, ok := keys["telegram-bot"]; ok && !regexp.MustCompile(`^1:[0-9a-f]{32}$`).Match(val) {
		return SinkConfig{}, nil, nil, ErrProtocol
	}
	return cfg, raw, keys, nil
}
func RunFixtureSink(ctx context.Context, role string, args []string) error {
	if !slices.Contains([]string{"smtp", "notification"}, role) || len(args) != 1 || !slices.Contains([]string{"serve", "healthcheck"}, args[0]) {
		return ErrProtocol
	}
	cfg, raw, keys, e := loadSinkConfig(role)
	if e != nil {
		return ErrProtocol
	}
	if args[0] == "healthcheck" {
		client := &http.Client{Timeout: time.Second}
		r, e := client.Get("http://127.0.0.1:" + portString(cfg.ControlPort) + "/healthz")
		if e != nil {
			return ErrProtocol
		}
		e = r.Body.Close()
		if e != nil || r.StatusCode != 200 {
			return ErrProtocol
		}
		return nil
	}
	private := "/run/umir-fixtures/private"
	if e := os.MkdirAll(private, 0o700); e != nil {
		return ErrProtocol
	}
	if e := os.Chmod(private, 0o700); e != nil {
		return ErrProtocol
	}
	binary, e := os.Executable()
	if e != nil {
		return ErrProtocol
	}
	b, e := os.ReadFile(binary)
	if e != nil {
		return ErrProtocol
	}
	sink, e := NewFixtureSink(role, cfg.Project, string(keys["control"]), private, SHA256(b), SHA256(raw))
	if e != nil {
		return ErrProtocol
	}
	defer sink.Close()
	cert, e := tls.X509KeyPair(keys[role+"-tls-cert"], keys[role+"-tls-key"])
	if e != nil {
		return ErrProtocol
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(keys["synthetic-ca.pem"]) {
		return ErrProtocol
	}
	leaf, e := x509.ParseCertificate(cert.Certificate[0])
	if e != nil {
		return ErrProtocol
	}
	host := "smtp"
	if role == "notification" {
		host = "api.telegram.org"
	}
	if _, e := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: host}); e != nil {
		return ErrProtocol
	}
	listener, e := net.Listen("tcp", ":"+portString(cfg.DeliveryPort))
	if e != nil {
		return ErrProtocol
	}
	control := &http.Server{Addr: ":" + portString(cfg.ControlPort), Handler: sink.ControlHandler(), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 16384, ErrorLog: nil}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan error, 2)
	go func() {
		var e error
		if role == "smtp" {
			e = ServeFixtureSMTP(child, listener, sink, cert, string(keys["smtp-username"]), string(keys["smtp-password"]))
		} else {
			e = ServeFixtureNotification(child, listener, sink, cert, string(keys["proxy-username"]), string(keys["proxy-password"]), string(keys["telegram-bot"]))
		}
		results <- e
	}()
	go func() {
		e := control.ListenAndServe()
		if errors.Is(e, http.ErrServerClosed) {
			e = nil
		}
		results <- e
	}()
	var result error
	consumed := 0
	select {
	case <-ctx.Done():
	case result = <-results:
		consumed++
	}
	cancel()
	shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	result = errors.Join(result, control.Shutdown(shutdown))
	for consumed < 2 {
		result = errors.Join(result, <-results)
		consumed++
	}
	if result != nil {
		return errors.New("synthetic sink stopped")
	}
	return nil
}
func portString(n int) string { return strconv.Itoa(n) }
