package faultproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	HoldBeforeForward = "hold-before-forward"
	DropResponse      = "drop-response"
	CorruptJSON       = "corrupt-json"
	CorruptKind       = "corrupt-kind"

	EventCaptured       = "captured"
	EventForwarded      = "forwarded"
	EventUpstreamResult = "upstream-result"
	EventReleased       = "released"
	EventDropped        = "dropped"
)

type Rule struct {
	ID              string        `json:"id"`
	Method          string        `json:"method,omitempty"`
	Path            string        `json:"path"`
	Action          string        `json:"action"`
	MaxUses         int           `json:"maxUses"`
	Timeout         time.Duration `json:"timeout,omitempty"`
	DetachOnRelease bool          `json:"detachOnRelease,omitempty"`
	DetachedTimeout time.Duration `json:"detachedTimeout,omitempty"`
}

type Event struct {
	RuleID     string    `json:"ruleID"`
	Name       string    `json:"name"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	StatusCode int       `json:"statusCode,omitempty"`
	At         time.Time `json:"at"`
}

type Traffic struct {
	Requests  int `json:"requests"`
	Forwarded int `json:"forwarded"`
}

type ruleState struct {
	rule      Rule
	uses      int
	released  chan struct{}
	releaseMu sync.Once
}

type Proxy struct {
	upstream *url.URL
	token    string
	client   *http.Client
	mu       sync.Mutex
	rules    map[string]*ruleState
	events   []Event
	traffic  map[string]Traffic
	changed  chan struct{}
}

func New(upstream, token string) (*Proxy, error) {
	parsed, err := url.Parse(upstream)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("upstream must be an HTTP URL without credentials, query, or fragment")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("control token is required")
	}
	return &Proxy{upstream: parsed, token: token, client: &http.Client{Timeout: 16 * time.Second}, rules: make(map[string]*ruleState), traffic: make(map[string]Traffic), changed: make(chan struct{})}, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.proxy(w, r)
}

func (p *Proxy) ControlHandler() http.Handler { return http.HandlerFunc(p.serveControl) }

func (p *Proxy) proxy(w http.ResponseWriter, r *http.Request) {
	p.recordTraffic(r.Method, r.URL.Path, false)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "request body exceeds fixture limit", http.StatusRequestEntityTooLarge)
		return
	}
	state := p.takeRule(r.Method, r.URL.Path)
	if state != nil {
		p.addEvent(state.rule, EventCaptured, 0)
		if state.rule.Action == HoldBeforeForward {
			timeout := state.rule.Timeout
			if timeout <= 0 {
				timeout = 15 * time.Second
			}
			timer := time.NewTimer(timeout)
			select {
			case <-state.released:
				stopTimer(timer)
				p.addEvent(state.rule, EventReleased, 0)
			case <-r.Context().Done():
				if !state.rule.DetachOnRelease {
					stopTimer(timer)
					return
				}
				select {
				case <-state.released:
					stopTimer(timer)
				case <-timer.C:
					p.addEvent(state.rule, "barrier-timeout", 0)
					return
				}
			case <-timer.C:
				p.addEvent(state.rule, "barrier-timeout", 0)
				http.Error(w, "fault barrier timed out", http.StatusGatewayTimeout)
				return
			}
		}
	}

	ctx := r.Context()
	if state != nil && state.rule.Action == HoldBeforeForward && state.rule.DetachOnRelease {
		timeout := state.rule.DetachedTimeout
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
		defer cancel()
	}
	upstreamURL := *p.upstream
	upstreamURL.Path = joinPath(p.upstream.Path, r.URL.Path)
	upstreamURL.RawPath = ""
	upstreamURL.RawQuery = r.URL.RawQuery
	request, err := http.NewRequestWithContext(ctx, r.Method, upstreamURL.String(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "cannot build upstream request", http.StatusBadGateway)
		return
	}
	request.Header = r.Header.Clone()
	request.Host = ""
	p.recordTraffic(r.Method, r.URL.Path, true)
	if state != nil {
		p.addEvent(state.rule, EventForwarded, 0)
	}
	response, err := p.client.Do(request)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		http.Error(w, "fault proxy upstream request failed", http.StatusBadGateway)
		return
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			log.Printf("close fault proxy upstream response: %v", err)
		}
	}()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 16<<20+1))
	if err != nil || len(responseBody) > 16<<20 {
		http.Error(w, "fault proxy upstream response exceeded fixture limit", http.StatusBadGateway)
		return
	}
	if state != nil {
		p.addEvent(state.rule, EventUpstreamResult, response.StatusCode)
	}
	if state != nil && state.rule.Action == DropResponse {
		p.addEvent(state.rule, EventDropped, response.StatusCode)
		if hijacker, ok := w.(http.Hijacker); ok {
			connection, _, err := hijacker.Hijack()
			if err == nil {
				if closeErr := connection.Close(); closeErr != nil {
					log.Printf("close dropped response connection: %v", closeErr)
				}
			}
		}
		return
	}
	if state != nil && state.rule.Action == CorruptJSON {
		responseBody = []byte(`{"kind":"fault-proxy-corrupt"`)
	} else if state != nil && state.rule.Action == CorruptKind {
		var payload map[string]any
		if json.Unmarshal(responseBody, &payload) == nil {
			payload["kind"] = "FaultProxyCorrupt"
			encoded, err := json.Marshal(payload)
			if err != nil {
				http.Error(w, "fault proxy could not encode corrupted payload", http.StatusBadGateway)
				return
			}
			responseBody = encoded
		}
	}
	for name, values := range response.Header {
		if strings.EqualFold(name, "Content-Length") || strings.EqualFold(name, "Transfer-Encoding") || strings.EqualFold(name, "Connection") {
			continue
		}
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(responseBody)))
	w.WriteHeader(response.StatusCode)
	if _, err := w.Write(responseBody); err != nil {
		log.Printf("write fault proxy response: %v", err)
	}
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func joinPath(base, request string) string {
	base = strings.TrimSuffix(base, "/")
	if base == "" {
		return request
	}
	return base + "/" + strings.TrimPrefix(request, "/")
}

func (p *Proxy) takeRule(method, path string) *ruleState {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, state := range p.rules {
		if state.uses >= state.rule.MaxUses || state.rule.Path != path || state.rule.Method != "" && state.rule.Method != method {
			continue
		}
		state.uses++
		return state
	}
	return nil
}

func (p *Proxy) addEvent(rule Rule, name string, status int) {
	p.mu.Lock()
	p.events = append(p.events, Event{RuleID: rule.ID, Name: name, Method: rule.Method, Path: rule.Path, StatusCode: status, At: time.Now().In(time.FixedZone("MSK", 3*60*60))})
	close(p.changed)
	p.changed = make(chan struct{})
	p.mu.Unlock()
}

func (p *Proxy) serveControl(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "http-fault-proxy"})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+p.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/control/rules":
		var rule Rule
		if err := decodeOne(r.Body, &rule); err != nil || !validRule(rule) {
			http.Error(w, "invalid fault rule", http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if _, exists := p.rules[rule.ID]; exists {
			http.Error(w, "duplicate fault rule", http.StatusConflict)
			return
		}
		p.rules[rule.ID] = &ruleState{rule: rule, released: make(chan struct{})}
		writeJSON(w, http.StatusCreated, map[string]string{"id": rule.ID})
	case r.Method == http.MethodPost && r.URL.Path == "/control/release":
		id := r.URL.Query().Get("id")
		p.mu.Lock()
		state := p.rules[id]
		p.mu.Unlock()
		if state == nil || state.rule.Action != HoldBeforeForward {
			http.Error(w, "unknown hold rule", http.StatusNotFound)
			return
		}
		state.releaseMu.Do(func() { close(state.released) })
		writeJSON(w, http.StatusOK, map[string]string{"released": id})
	case r.Method == http.MethodGet && r.URL.Path == "/control/events":
		p.waitEvents(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/control/stats":
		method, path := r.URL.Query().Get("method"), r.URL.Query().Get("path")
		if method == "" || !strings.HasPrefix(path, "/") {
			http.Error(w, "invalid traffic query", http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		traffic := p.traffic[method+"\x00"+path]
		p.mu.Unlock()
		writeJSON(w, http.StatusOK, traffic)
	case r.Method == http.MethodPost && r.URL.Path == "/control/reset-stats":
		p.mu.Lock()
		p.traffic = make(map[string]Traffic)
		p.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
	default:
		http.NotFound(w, r)
	}
}

func (p *Proxy) recordTraffic(method, path string, forwarded bool) {
	p.mu.Lock()
	key := method + "\x00" + path
	traffic := p.traffic[key]
	if forwarded {
		traffic.Forwarded++
	} else {
		traffic.Requests++
	}
	p.traffic[key] = traffic
	p.mu.Unlock()
}

func (p *Proxy) waitEvents(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	want := r.URL.Query().Get("event")
	waitMs, err := strconv.Atoi(r.URL.Query().Get("waitMs"))
	if id == "" || want == "" || err != nil || waitMs < 0 || waitMs > 30000 {
		http.Error(w, "invalid event wait", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(waitMs)*time.Millisecond)
	defer cancel()
	for {
		p.mu.Lock()
		for _, event := range p.events {
			if event.RuleID == id && event.Name == want {
				p.mu.Unlock()
				writeJSON(w, http.StatusOK, event)
				return
			}
		}
		changed := p.changed
		p.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			writeJSON(w, http.StatusRequestTimeout, map[string]string{"error": "event wait expired"})
			return
		}
	}
}

func validRule(rule Rule) bool {
	if strings.TrimSpace(rule.ID) == "" || !strings.HasPrefix(rule.Path, "/") || rule.MaxUses < 1 || rule.MaxUses > 100 {
		return false
	}
	switch rule.Action {
	case HoldBeforeForward, DropResponse, CorruptJSON, CorruptKind:
	default:
		return false
	}
	if rule.Action == HoldBeforeForward && rule.Timeout > 30*time.Second {
		return false
	}
	return true
}

func decodeOne(body io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request must contain one JSON value")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write fault proxy control response: %v", err)
	}
}

type ControlClient struct {
	base   string
	token  string
	client *http.Client
}

func NewControlClient(base, token string) *ControlClient {
	return &ControlClient{base: strings.TrimRight(base, "/"), token: token, client: &http.Client{Timeout: 32 * time.Second}}
}

func (c *ControlClient) Arm(ctx context.Context, rule Rule) error {
	return c.doJSON(ctx, http.MethodPost, "/control/rules", rule, nil)
}

func (c *ControlClient) Release(ctx context.Context, id string) error {
	return c.doJSON(ctx, http.MethodPost, "/control/release?id="+url.QueryEscape(id), nil, nil)
}

func (c *ControlClient) WaitEvent(ctx context.Context, id, name string, timeout time.Duration) (Event, error) {
	var event Event
	path := "/control/events?id=" + url.QueryEscape(id) + "&event=" + url.QueryEscape(name) + "&waitMs=" + strconv.FormatInt(timeout.Milliseconds(), 10)
	err := c.doJSON(ctx, http.MethodGet, path, nil, &event)
	return event, err
}

func (c *ControlClient) Stats(ctx context.Context, method, path string) (Traffic, error) {
	var traffic Traffic
	query := url.Values{}
	query.Set("method", method)
	query.Set("path", path)
	err := c.doJSON(ctx, http.MethodGet, "/control/stats?"+query.Encode(), nil, &traffic)
	return traffic, err
}

func (c *ControlClient) ResetStats(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodPost, "/control/reset-stats", nil, nil)
}

func (c *ControlClient) doJSON(ctx context.Context, method, path string, body, output any) (resultErr error) {
	var requestBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		requestBody = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.base+path, requestBody)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, response.Body.Close()) }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, err := io.ReadAll(io.LimitReader(response.Body, 4096))
		if err != nil {
			return fmt.Errorf("read fault control failure: %w", err)
		}
		return fmt.Errorf("fault control returned %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	if output != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output)
	}
	return nil
}
