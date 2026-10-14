package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JustIkra/umir-factory/tests/http/internal/faultproxy"
)

//go:embed backend-private.json backend-run.schema.json
var backendPrivateWire embed.FS

func canonicalBackendURL(value string) bool {
	if len(value) > 32 {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Hostname() != "127.0.0.1" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	port, err := strconv.Atoi(u.Port())
	return err == nil && port > 0 && port <= 65535 && value == "http://127.0.0.1:"+strconv.Itoa(port)
}
func validatePrivateURLs(run backendRunDescriptor) error {
	if len(run.PrivateServiceURLs) != 5 || !canonicalBackendURL(run.GatewayURL) {
		return faultproxy.ErrProtocol
	}
	seen := map[string]bool{run.GatewayURL: true}
	for _, name := range []string{"smtp", "notification"} {
		u := run.SinkURLs[name]
		if !canonicalBackendURL(u) || seen[u] {
			return faultproxy.ErrProtocol
		}
		seen[u] = true
	}
	for _, name := range backendOwners {
		u := run.PrivateServiceURLs[name]
		if !canonicalBackendURL(u) || seen[u] {
			return faultproxy.ErrProtocol
		}
		seen[u] = true
	}
	return nil
}
func backendPrivatePort(target string) int {
	if target == "gateway" {
		return 8082
	}
	return 8080
}

func (c *backendClient) readOwned(ctx context.Context, args ...string) ([]byte, error) {
	child, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if child.Err() != nil {
		return nil, errors.New("owned discovery unavailable")
	}
	raw, err := c.execute(child, nil, args...)
	if err != nil || len(raw) > 262144 || child.Err() != nil {
		return nil, errors.New("owned discovery unavailable")
	}
	return raw, nil
}
func (c *backendClient) capturedContainers() error {
	if c.containers != nil {
		return nil
	}
	raw, err := faultproxy.ReadPrivateFile(filepath.Join(c.run.EvidenceDirectory, "evidence.json"), 16<<20)
	if err != nil {
		return errors.New("initial ownership evidence unavailable")
	}
	if _, err := faultproxy.StrictJSON(raw, 32); err != nil {
		return faultproxy.ErrProtocol
	}
	var ev struct {
		Project    string `json:"project"`
		Containers []struct {
			ID      string `json:"ID"`
			Service string `json:"Service"`
			ImageID string `json:"imageID"`
		} `json:"containers"`
	}
	if json.Unmarshal(raw, &ev) != nil || ev.Project != c.run.Project || len(ev.Containers) != 7 {
		return errors.New("OwnershipMismatch")
	}
	ids := map[string]string{}
	seen := map[string]bool{}
	for _, v := range ev.Containers {
		if !slices.Contains(backendTargets, v.Service) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(v.ID) || seen[v.ID] || ids[v.Service] != "" || v.ImageID != c.run.Images[v.Service].ID {
			return errors.New("OwnershipMismatch")
		}
		ids[v.Service] = v.ID
		seen[v.ID] = true
	}
	c.containers = ids
	return nil
}

type backendPortBinding struct{ HostIP, HostPort string }
type backendProbeInspection struct {
	ID, Image, Project, Service, Run string
	Running                          bool
	Health                           string
	Ports                            map[string][]backendPortBinding
}

const backendProbeFormat = `{"id":{{json .Id}},"image":{{json .Image}},"running":{{json .State.Running}},"health":{{if .State.Health}}{{json .State.Health.Status}}{{else}}""{{end}},"project":{{json (index .Config.Labels "com.docker.compose.project")}},"service":{{json (index .Config.Labels "com.docker.compose.service")}},"run":{{json (index .Config.Labels "io.umir.backend.run")}},"ports":{{json .NetworkSettings.Ports}}}`

func (c *backendClient) inspectProbe(ctx context.Context, target string) (backendProbeInspection, error) {
	var info backendProbeInspection
	id, err := c.ownership(ctx, target)
	if err != nil {
		return info, err
	}
	raw, err := c.readOwned(ctx, "container", "inspect", "--format", backendProbeFormat, id)
	if err != nil {
		return info, err
	}
	if _, err := faultproxy.StrictJSON(raw, 16); err != nil {
		return info, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&info) != nil || info.ID != id || info.ID != c.containers[target] && !c.requalifying[target] || info.Image != c.run.Images[target].ID || !info.Running || info.Project != c.run.Project || info.Service != target || info.Run != c.run.Project || slices.Contains(backendOwners, target) && info.Health != "healthy" {
		return info, errors.New("OwnershipMismatch")
	}
	return info, nil
}
func (c *backendClient) probeMappings(info backendProbeInspection, target string, allowNew bool) (map[int]string, error) {
	expected := map[int]string{}
	if slices.Contains(backendOwners, target) {
		expected[backendPrivatePort(target)] = c.run.PrivateServiceURLs[target]
	}
	if target == "gateway" {
		expected[8080] = c.run.GatewayURL
	}
	if target == "smtp" {
		expected[8025] = c.run.SinkURLs[target]
	}
	if target == "notification" {
		expected[8026] = c.run.SinkURLs[target]
	}
	found := map[int]string{}
	seen := map[string]bool{}
	for key, bindings := range info.Ports {
		if len(bindings) == 0 {
			continue
		}
		parts := strings.Split(key, "/")
		if len(parts) != 2 || parts[1] != "tcp" || len(bindings) != 1 {
			return nil, errors.New("private mapping rejected")
		}
		port, err := strconv.Atoi(parts[0])
		_, ok := expected[port]
		if err != nil || !ok || strconv.Itoa(port) != parts[0] {
			return nil, errors.New("private target rejected")
		}
		binding := bindings[0]
		endpoint := "http://" + binding.HostIP + ":" + binding.HostPort
		if !canonicalBackendURL(endpoint) || binding.HostIP != "127.0.0.1" || seen[endpoint] || !allowNew && endpoint != expected[port] {
			return nil, errors.New("private mapping rejected")
		}
		found[port] = endpoint
		seen[endpoint] = true
	}
	if len(found) != len(expected) {
		return nil, errors.New("private mapping incomplete")
	}
	return found, nil
}
func (c *backendClient) discoverProbe(ctx context.Context, target string, allowNew bool) (backendProbeInspection, map[int]string, error) {
	info, err := c.inspectProbe(ctx, target)
	if err != nil {
		return info, nil, err
	}
	mappings, err := c.probeMappings(info, target, allowNew)
	if err != nil {
		return info, nil, err
	}
	for port, endpoint := range mappings {
		raw, err := c.readOwned(ctx, c.compose("port", target, strconv.Itoa(port))...)
		if err != nil || len(raw) > 128 || "http://"+strings.TrimSpace(string(raw)) != endpoint {
			return info, nil, errors.New("fresh endpoint mismatch")
		}
	}
	after, err := c.inspectProbe(ctx, target)
	if err != nil || after.ID != info.ID {
		return info, nil, errors.New("OwnershipMismatch")
	}
	afterMappings, err := c.probeMappings(after, target, allowNew)
	if err != nil || !equalBackendMappings(mappings, afterMappings) {
		return info, nil, errors.New("mapping changed during discovery")
	}
	return info, mappings, nil
}
func equalBackendMappings(a, b map[int]string) bool {
	if len(a) != len(b) {
		return false
	}
	for p, u := range a {
		if b[p] != u {
			return false
		}
	}
	return true
}
func (c *backendClient) qualifyProbe(ctx context.Context, target string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if _, err := backendWaitMilliseconds(ctx, 0); err != nil {
		return err
	}
	if len(c.unqualified) > 0 {
		return errors.New("private owner requires qualification")
	}
	if validatePrivateURLs(c.run) != nil || !slices.Contains(backendTargets, target) {
		return faultproxy.ErrProtocol
	}
	if err := c.capturedContainers(); err != nil {
		return err
	}
	raw, err := c.readOwned(ctx, c.compose("ps", "--all", "--quiet")...)
	if err != nil {
		return err
	}
	ids := strings.Fields(string(raw))
	if len(ids) != 7 {
		return errors.New("OwnershipMismatch")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] || !slices.ContainsFunc(backendTargets, func(t string) bool { return c.containers[t] == id }) {
			return errors.New("OwnershipMismatch")
		}
		seen[id] = true
	}
	for _, name := range backendTargets {
		info, err := c.inspectProbe(ctx, name)
		if err != nil {
			return err
		}
		if _, err := c.probeMappings(info, name, false); err != nil {
			return err
		}
	}
	if _, _, err := c.discoverProbe(ctx, target, false); err != nil {
		return err
	}
	if slices.Contains(backendOwners, target) {
		prior := c.instances[target]
		if _, err := c.control(ctx, target, "fixture.ready", map[string]any{}); err != nil {
			return err
		}
		if prior != "" && prior != c.instances[target] {
			c.instances[target] = prior
			return errors.New("unqualified owner instance")
		}
		if _, _, err := c.discoverProbe(ctx, target, false); err != nil {
			return err
		}
	}
	return nil
}
func (c *backendClient) publicProbe(ctx context.Context) (func(), error) {
	release, err := c.lockProbeState(ctx)
	if err != nil {
		return nil, err
	}
	if err := c.qualifyProbe(ctx, "gateway"); err != nil {
		release()
		return nil, err
	}
	c.publicMu.Lock()
	if c.publicActive == 0 {
		c.publicIdle = make(chan struct{})
	}
	c.publicActive++
	c.publicMu.Unlock()
	release()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.publicMu.Lock()
			defer c.publicMu.Unlock()
			c.publicActive--
			if c.publicActive == 0 {
				close(c.publicIdle)
			}
		})
	}, nil
}
func backendProbeTransport(qualifiedAddress string) *http.Transport {
	return &http.Transport{Proxy: nil, DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: 16384, ResponseHeaderTimeout: 5 * time.Second, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != qualifiedAddress {
			return nil, errors.New("outside connection rejected")
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", qualifiedAddress)
	}}
}
func backendPinnedHTTP(ctx context.Context, endpoint, method, route string, raw []byte, headers http.Header) (backendPublicReply, error) {
	if !canonicalBackendURL(endpoint) || len(route) > 16384 || (!strings.HasPrefix(route, "/internal/") && !strings.HasPrefix(route, "/fixture/")) || strings.ContainsAny(route, "\r\n") || len(raw) > 1<<20 {
		return backendPublicReply{}, faultproxy.ErrProtocol
	}
	base, err := url.Parse(endpoint)
	if err != nil {
		return backendPublicReply{}, faultproxy.ErrProtocol
	}
	child, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	transport := backendProbeTransport(base.Host)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(child, method, endpoint+route, bytes.NewReader(raw))
	if err != nil {
		return backendPublicReply{}, faultproxy.ErrProtocol
	}
	if req.URL.Scheme != "http" || req.URL.Host != base.Host || req.URL.User != nil || req.URL.Fragment != "" {
		return backendPublicReply{}, faultproxy.ErrProtocol
	}
	size := 0
	for k, values := range headers {
		size += len(k)
		for _, v := range values {
			size += len(v)
		}
	}
	if size > 16384 {
		return backendPublicReply{}, faultproxy.ErrProtocol
	}
	req.Header = headers.Clone()
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return backendPublicReply{}, errors.New("private transport outcome unknown")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	err = errors.Join(err, res.Body.Close())
	if err != nil || len(body) > 1<<20 || res.StatusCode >= 300 && res.StatusCode < 400 {
		return backendPublicReply{}, errors.New("private response rejected")
	}
	return backendPublicReply{res.StatusCode, slices.Clone(body), res.Header.Clone()}, nil
}

type backendPrivateObservation struct {
	Target, Operation           string
	Status                      int
	RequestValid, ResponseValid bool
}
type backendPrivateCredential struct {
	Name    string
	Token   backendAuthToken
	Headers http.Header
}

func privateOperation(id string) ([]byte, backendPublicOperation, string, error) {
	raw, err := backendPrivateWire.ReadFile("backend-private.json")
	if err != nil || faultproxy.SHA256(raw) != "f1a46a199c5c616523ff24ed0c43a0ae92b43edd93e1264a854fd766030aa1c1" {
		return nil, backendPublicOperation{}, "", faultproxy.ErrProtocol
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil, backendPublicOperation{}, "", faultproxy.ErrProtocol
	}
	for path, methods := range doc.Paths {
		for method, v := range methods {
			if !slices.Contains([]string{"get", "post", "put", "patch", "delete"}, method) {
				continue
			}
			var op struct {
				ID        string                     `json:"operationId"`
				Owner     string                     `json:"x-owner"`
				Request   json.RawMessage            `json:"requestBody"`
				Responses map[string]json.RawMessage `json:"responses"`
				Security  []map[string][]string      `json:"security"`
			}
			if json.Unmarshal(v, &op) != nil {
				return nil, backendPublicOperation{}, "", faultproxy.ErrProtocol
			}
			if op.ID == id {
				parts := strings.Split(path, "/")
				if len(parts) < 4 || !slices.Contains(backendOwners, parts[2]) || strings.ToLower(op.Owner) != parts[2] && op.Owner != "Runtime" {
					return nil, backendPublicOperation{}, "", faultproxy.ErrProtocol
				}
				needsJWT := len(op.Security) == 0
				for _, v := range op.Security {
					if _, ok := v["serviceJWT"]; ok {
						needsJWT = true
					}
				}
				return raw, backendPublicOperation{Path: path, Method: strings.ToUpper(method), ID: id, Request: op.Request, Responses: op.Responses, NeedsJWT: needsJWT}, parts[2], nil
			}
		}
	}
	return nil, backendPublicOperation{}, "", errors.New("unsupported private operation")
}
func targetCredential(target, name string) bool {
	allowed := map[string][]string{"auth": {"gateway-auth", "serving-auth"}, "warehouse": {"auth-warehouse", "gateway-warehouse", "serving-warehouse"}, "serving": {"auth-serving", "gateway-serving"}, "gateway": {"auth-gateway"}, "feedback": {"gateway-feedback"}}
	return slices.Contains(allowed[target], name)
}
func (c *backendClient) privateCall(ctx context.Context, target, operation string, params map[string]string, query url.Values, payload any, credential backendPrivateCredential, status int) (backendPublicReply, error) {
	var raw []byte
	var err error
	if payload != nil {
		raw, err = json.Marshal(payload)
		if err != nil {
			return backendPublicReply{}, faultproxy.ErrProtocol
		}
	}
	return c.privateBytes(ctx, target, operation, params, query, raw, credential.Headers, credential, false, status)
}
func (c *backendClient) privateReject(ctx context.Context, target, operation string, params map[string]string, query url.Values, raw []byte, headers http.Header, credential backendPrivateCredential, status int) (backendPublicReply, error) {
	if status < 400 || status > 599 {
		return backendPublicReply{}, faultproxy.ErrProtocol
	}
	return c.privateBytes(ctx, target, operation, params, query, raw, headers, credential, true, status)
}
func (c *backendClient) privateBytes(ctx context.Context, target, operation string, params map[string]string, query url.Values, raw []byte, extra http.Header, credential backendPrivateCredential, rejection bool, status int) (backendPublicReply, error) {
	release, lockErr := c.lockPrivate(ctx)
	if lockErr != nil {
		return backendPublicReply{}, lockErr
	}
	defer release()
	wire, op, owner, err := privateOperation(operation)
	if err != nil || target != owner || !targetCredential(target, credential.Name) || len(raw) > 1<<20 {
		return backendPublicReply{}, faultproxy.ErrProtocol
	}
	route := op.Path
	for name, v := range params {
		if v == "" || strings.ContainsAny(v, "/\\?#") || v == "." || v == ".." {
			return backendPublicReply{}, faultproxy.ErrProtocol
		}
		route = strings.ReplaceAll(route, "{"+name+"}", url.PathEscape(v))
	}
	if strings.ContainsAny(route, "{}") {
		return backendPublicReply{}, faultproxy.ErrProtocol
	}
	valid := len(raw) == 0
	if len(op.Request) > 0 {
		valid = faultproxy.ValidateSchema(wire, schemaPointer(op.Path, op.Method, true, 0), raw, 32) == nil
	}
	if !rejection && op.NeedsJWT && credential.Token.value == "" {
		return backendPublicReply{}, errors.New("auth-issued token required")
	}
	if !rejection && !valid {
		return backendPublicReply{}, errors.New("private request schema rejected")
	}
	if err := c.qualifyProbe(ctx, target); err != nil {
		return backendPublicReply{}, err
	}
	secret, err := c.credential(credential.Name)
	if err != nil {
		return backendPublicReply{}, err
	}
	defer clear(secret)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).Match(secret) {
		return backendPublicReply{}, faultproxy.ErrProtocol
	}
	headers := http.Header{"X-Umir-Service-Key": []string{string(secret)}}
	if credential.Token.value != "" {
		headers.Set("Authorization", "Bearer "+credential.Token.value)
	}
	if !rejection {
		for k, values := range extra {
			if !slices.Contains([]string{"X-Umir-Session", "X-Umir-Preauth", "X-Umir-Csrf-Token", "X-Umir-Activity"}, http.CanonicalHeaderKey(k)) || len(values) != 1 || len(values[0]) > 16384 {
				return backendPublicReply{}, faultproxy.ErrProtocol
			}
		}
	}
	for k, v := range extra {
		headers[k] = slices.Clone(v)
	}
	if len(query) > 0 {
		route += "?" + query.Encode()
	}
	reply, err := backendPinnedHTTP(ctx, c.run.PrivateServiceURLs[target], op.Method, route, raw, headers)
	ob := backendPrivateObservation{Target: target, Operation: operation, Status: reply.Status, RequestValid: valid}
	defer func() { c.privateObservations = append(c.privateObservations, ob) }()
	if err != nil {
		return backendPublicReply{}, err
	}
	if reply.Status != status {
		return backendPublicReply{}, errors.New("private status mismatch")
	}
	pointer := schemaPointer(op.Path, op.Method, false, status)
	if _, ok := op.Responses[strconv.Itoa(status)]; !ok {
		pointer = strings.ReplaceAll(pointer, "/responses/"+strconv.Itoa(status)+"/", "/responses/default/")
	}
	if status == 204 && len(reply.Body) == 0 {
		if _, documented := op.Responses["204"]; !documented {
			return backendPublicReply{}, errors.New("undocumented no-body response")
		}
		ob.ResponseValid = true
		return reply, nil
	}
	if faultproxy.ValidateSchema(wire, pointer, reply.Body, 32) != nil {
		return backendPublicReply{}, errors.New("private response schema rejected")
	}
	ob.ResponseValid = true
	return reply, nil
}

func (c *backendClient) privateAuthLogin(ctx context.Context, login string, password []byte, command string) (string, error) {
	cred := backendPrivateCredential{Name: "gateway-auth"}
	pre, err := c.privateCall(ctx, "auth", "authFacadecsrf", nil, nil, nil, cred, 200)
	if err != nil {
		return "", err
	}
	var preauth struct{ CSRFToken, PreauthToken string }
	if json.Unmarshal(pre.Body, &preauth) != nil || preauth.CSRFToken == "" || preauth.PreauthToken == "" {
		return "", faultproxy.ErrProtocol
	}
	raw, err := json.Marshal(map[string]any{"login": login, "password": string(password), "rememberMe": false, "commandID": command})
	if err != nil {
		return "", faultproxy.ErrProtocol
	}
	headers := http.Header{"X-Umir-Preauth": []string{preauth.PreauthToken}, "X-Umir-CSRF-Token": []string{preauth.CSRFToken}}
	reply, err := c.privateAuthSession(ctx, raw, headers, cred)
	if err != nil {
		return "", err
	}
	var issued struct{ SessionToken string }
	if json.Unmarshal(reply.Body, &issued) != nil || issued.SessionToken == "" {
		return "", faultproxy.ErrProtocol
	}
	return issued.SessionToken, nil
}
func (c *backendClient) privateAuthSession(ctx context.Context, raw []byte, headers http.Header, cred backendPrivateCredential) (backendPublicReply, error) {
	return c.privateBytes(ctx, "auth", "authFacadelogin", nil, nil, raw, headers, cred, false, 200)
}
func (c *backendClient) scopedAuthToken(ctx context.Context, sessionToken, restaurant, operation string, audiences []string) (backendAuthToken, error) {
	reply, err := c.privateCall(ctx, "auth", "authorize", nil, nil, map[string]any{"sessionToken": sessionToken, "restaurantID": restaurant, "operation": operation, "audiences": slices.Clone(audiences), "activity": "background"}, backendPrivateCredential{Name: "gateway-auth"}, 200)
	if err != nil {
		return backendAuthToken{}, err
	}
	var result struct{ Token string }
	if json.Unmarshal(reply.Body, &result) != nil || result.Token == "" {
		return backendAuthToken{}, faultproxy.ErrProtocol
	}
	return backendAuthToken{value: result.Token}, nil
}

func (c *backendClient) commitRestartProbe(ctx context.Context, target, expectedContainer string, before, after fixtureClock) error {
	if !c.requalifying[target] || expectedContainer == "" || validateBackendRestartClock(before, after) != nil {
		return errors.New("unqualified restart")
	}
	info, mappings, err := c.discoverProbe(ctx, target, true)
	if err != nil {
		return err
	}
	if info.ID != expectedContainer {
		return errors.New("OwnershipMismatch")
	}
	run := c.run
	run.PrivateServiceURLs = map[string]string{}
	for name, endpoint := range c.run.PrivateServiceURLs {
		run.PrivateServiceURLs[name] = endpoint
	}
	run.PrivateServiceURLs[target] = mappings[backendPrivatePort(target)]
	if target == "gateway" && mappings[8080] != run.GatewayURL {
		return errors.New("public origin mapping changed")
	}
	if validatePrivateURLs(run) != nil {
		return faultproxy.ErrProtocol
	}
	ids := map[string]string{}
	for name, id := range c.containers {
		ids[name] = id
	}
	ids[target] = info.ID
	snapshot := struct {
		Run           backendRunDescriptor `json:"run"`
		Containers    map[string]string    `json:"containers"`
		Target        string               `json:"target"`
		Before, After fixtureClock
	}{run, ids, target, before, after}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return faultproxy.ErrProtocol
	}
	path := filepath.Join(run.Fixtures.HarnessPrivateRoot, "private-probe-state.json")
	if err := writeBackendPrivateSnapshot(path, raw); err != nil {
		return err
	}
	c.run = run
	c.containers = ids
	delete(c.unqualified, target)
	return nil
}
func writeBackendPrivateSnapshot(path string, raw []byte) error {
	if !filepath.IsAbs(path) {
		return faultproxy.ErrProtocol
	}
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return faultproxy.ErrProtocol
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return faultproxy.ErrProtocol
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return faultproxy.ErrProtocol
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".private-probe-")
	if err != nil {
		return faultproxy.ErrProtocol
	}
	temp := f.Name()
	_, writeErr := f.Write(raw)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return errors.Join(faultproxy.ErrProtocol, os.Remove(temp))
	}
	if err := os.Rename(temp, path); err != nil {
		return errors.Join(faultproxy.ErrProtocol, os.Remove(temp))
	}
	return nil
}

func (c *backendClient) lockPrivate(ctx context.Context) (func(), error) {
	release, err := c.lockProbeState(ctx)
	if err != nil {
		return nil, err
	}
	c.publicMu.Lock()
	active, idle := c.publicActive, c.publicIdle
	c.publicMu.Unlock()
	if active > 0 {
		select {
		case <-idle:
		case <-ctx.Done():
			release()
			return nil, errors.New("private probe budget exhausted")
		}
	}
	return release, nil
}

func (c *backendClient) lockProbeState(ctx context.Context) (func(), error) {
	if ctx.Err() != nil {
		return nil, errors.New("private probe budget exhausted")
	}
	c.privateOnce.Do(func() { c.privateGate = make(chan struct{}, 1) })
	select {
	case c.privateGate <- struct{}{}:
		return func() { <-c.privateGate }, nil
	case <-ctx.Done():
		return nil, errors.New("private probe budget exhausted")
	}
}

type backendAuthToken struct{ value string }

func (c *backendClient) serviceAuthToken(ctx context.Context, restaurant, operation, epoch string) (backendAuthToken, error) {
	return c.issueAuthToken(ctx, "serviceToken", map[string]any{"restaurantID": restaurant, "operation": operation, "epoch": epoch})
}
func (c *backendClient) anonymousAuthToken(ctx context.Context) (backendAuthToken, error) {
	return c.issueAuthToken(ctx, "anonymousToken", map[string]any{"operation": "feedback.anonymous.submit"})
}
func (c *backendClient) issueAuthToken(ctx context.Context, operation string, payload any) (backendAuthToken, error) {
	reply, err := c.privateCall(ctx, "auth", operation, nil, nil, payload, backendPrivateCredential{Name: "gateway-auth"}, 200)
	if err != nil {
		return backendAuthToken{}, err
	}
	var token struct{ Token string }
	if json.Unmarshal(reply.Body, &token) != nil || token.Token == "" {
		return backendAuthToken{}, faultproxy.ErrProtocol
	}
	return backendAuthToken{value: token.Token}, nil
}

func (c *backendClient) privateReplay(ctx context.Context, target, operation string, params map[string]string, query url.Values, raw []byte, credential backendPrivateCredential, status int) (backendPublicReply, error) {
	return c.privateBytes(ctx, target, operation, params, query, slices.Clone(raw), credential.Headers, credential, false, status)
}
