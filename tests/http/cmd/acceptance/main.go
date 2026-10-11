package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type config struct {
	phase           string
	gatewayURL      string
	servingFault    string
	warehouseFault  string
	controlToken    string
	factoryDir      string
	composeProject  string
	composeOverride string
}

type scenarioResult struct {
	Outcomes     []string         `json:"outcomes"`
	Observations []scenarioRecord `json:"observations"`
}

type scenarioRecord struct {
	ID           string            `json:"id"`
	Observations map[string]string `json:"observations"`
}

type suite struct {
	cfg       config
	client    *http.Client
	serving   *faultControl
	warehouse *faultControl
	result    scenarioResult
	current   string
}

type faultControl struct {
	base   string
	token  string
	client *http.Client
}

type proxyTraffic struct {
	Requests  int `json:"requests"`
	Forwarded int `json:"forwarded"`
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	var cfg config
	flags := flag.NewFlagSet("http-acceptance", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&cfg.phase, "phase", "", "normal or faults scenario group")
	flags.StringVar(&cfg.gatewayURL, "gateway-url", "", "public gateway URL")
	flags.StringVar(&cfg.servingFault, "serving-fault-control-url", "", "serving-to-warehouse fault fixture control URL")
	flags.StringVar(&cfg.warehouseFault, "warehouse-fault-control-url", "", "gateway-to-serving fault fixture control URL")
	flags.StringVar(&cfg.controlToken, "control-token", "", "ephemeral control token")
	flags.StringVar(&cfg.factoryDir, "factory-dir", "", "absolute factory checkout")
	flags.StringVar(&cfg.composeProject, "compose-project", "", "owned Compose project for restart scenarios")
	flags.StringVar(&cfg.composeOverride, "compose-override", "", "optional Compose override for restart scenarios")
	if err := flags.Parse(args); err != nil {
		diagnostic(stderr, "invalid acceptance arguments: %v\n", err)
		return 2
	}
	if flags.NArg() != 0 || cfg.phase != "normal" && cfg.phase != "faults" {
		diagnostic(stderr, "--phase must be normal or faults\n")
		return 2
	}
	if err := validateURL(cfg.gatewayURL, true); err != nil {
		diagnostic(stderr, "invalid gateway URL: %v\n", err)
		return 2
	}
	restartCfg := restartConfig{factoryDir: cfg.factoryDir, project: cfg.composeProject, override: cfg.composeOverride}
	if err := validateRestartConfig(restartCfg, "warehouse"); err != nil {
		diagnostic(stderr, "invalid owned Compose restart context: %v\n", err)
		return 2
	}
	if !composeProjectMatchesPhase(cfg.composeProject, cfg.phase) {
		diagnostic(stderr, "Compose project suffix does not match acceptance phase %q\n", cfg.phase)
		return 2
	}
	if cfg.phase == "faults" {
		if cfg.composeOverride == "" {
			diagnostic(stderr, "fault scenarios require --compose-override for the owned six-service topology\n")
			return 2
		}
		if err := validateURL(cfg.servingFault, true); err != nil {
			diagnostic(stderr, "invalid serving fault control URL: %v\n", err)
			return 2
		}
		if err := validateURL(cfg.warehouseFault, true); err != nil {
			diagnostic(stderr, "invalid warehouse fault control URL: %v\n", err)
			return 2
		}
		if cfg.controlToken == "" {
			diagnostic(stderr, "--control-token is required for fault scenarios\n")
			return 2
		}
	}
	s := &suite{cfg: cfg, client: &http.Client{Timeout: 16 * time.Second}}
	if cfg.phase == "faults" {
		s.serving = &faultControl{base: strings.TrimRight(cfg.servingFault, "/"), token: cfg.controlToken, client: &http.Client{Timeout: 32 * time.Second}}
		s.warehouse = &faultControl{base: strings.TrimRight(cfg.warehouseFault, "/"), token: cfg.controlToken, client: &http.Client{Timeout: 32 * time.Second}}
	}
	var err error
	if cfg.phase == "normal" {
		err = s.runNormal()
	} else {
		err = s.runFaults()
	}
	data, marshalErr := json.Marshal(s.result)
	if marshalErr != nil {
		diagnostic(stderr, "encode acceptance evidence: %v\n", marshalErr)
		return 1
	}
	if _, writeErr := fmt.Fprintf(stdout, "%s\n", data); writeErr != nil {
		diagnostic(stderr, "write acceptance evidence: %v\n", writeErr)
		return 1
	}
	if err != nil {
		diagnostic(stderr, "HTTP scenario %s failed: %v\n", s.current, err)
		return 1
	}
	return 0
}

func diagnostic(writer io.Writer, format string, values ...any) {
	if _, err := fmt.Fprintf(writer, format, values...); err != nil {
		return
	}
}

func errorWithCause(message string, cause error) error {
	if cause == nil {
		return errors.New(message)
	}
	return fmt.Errorf("%s: %w", message, cause)
}

func validateURL(value string, requireLoopback bool) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("must be an HTTP URL with a host and no credentials, query, or fragment")
	}
	if requireLoopback {
		host, _, splitErr := net.SplitHostPort(parsed.Host)
		if splitErr != nil {
			host = parsed.Hostname()
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("must use a loopback host")
		}
	}
	return nil
}

func (s *suite) runNormal() error {
	scenarios := []func() error{s.h01, s.h02, s.h03, s.h04, s.h05h06, s.h07, s.h09, s.h10, s.h11, s.h12, s.h21, s.h22, s.h23, s.h24}
	ids := []string{"H01", "H02", "H03", "H04", "H05/H06", "H07", "H09", "H10", "H11", "H12", "H21", "H22", "H23", "H24"}
	for index, scenario := range scenarios {
		s.current = ids[index]
		if err := scenario(); err != nil {
			return err
		}
		if index < len(scenarios)-1 {
			if err := s.resetMemories(); err != nil {
				return fmt.Errorf("reset process memory after %s: %w", s.result.Outcomes[len(s.result.Outcomes)-1], err)
			}
		}
	}
	return nil
}

func (s *suite) runFaults() error {
	scenarios := []func() error{s.h13, s.h14, s.h15, s.h17, s.h08, s.h18, s.h19, s.h20}
	ids := []string{"H13", "H14", "H15", "H17", "H08", "H18", "H19", "H20"}
	for index, scenario := range scenarios {
		s.current = ids[index]
		if err := scenario(); err != nil {
			return err
		}
		if index < len(scenarios)-1 && ids[index] != "H17" {
			if err := s.resetMemories(); err != nil {
				return fmt.Errorf("reset process memory after %s: %w", s.result.Outcomes[len(s.result.Outcomes)-1], err)
			}
		}
	}
	return nil
}

func (s *suite) resetMemories() error {
	config := restartConfig{factoryDir: s.cfg.factoryDir, project: s.cfg.composeProject, override: s.cfg.composeOverride}
	if err := restart(context.Background(), config, "warehouse"); err != nil {
		return err
	}
	if err := waitHealth(s.client, s.cfg.gatewayURL+"/api/warehouse/healthz", 20*time.Second); err != nil {
		return err
	}
	if err := restart(context.Background(), config, "serving"); err != nil {
		return err
	}
	if err := waitHealth(s.client, s.cfg.gatewayURL+"/api/serving/healthz", 20*time.Second); err != nil {
		return err
	}
	if s.serving != nil {
		if err := s.serving.resetStats(context.Background()); err != nil {
			return err
		}
		if err := s.warehouse.resetStats(context.Background()); err != nil {
			return err
		}
	}
	return nil
}

func (s *suite) record(id string, observations map[string]string) {
	s.result.Outcomes = append(s.result.Outcomes, id+":passed")
	s.result.Observations = append(s.result.Observations, scenarioRecord{ID: id, Observations: observations})
}

type response struct {
	status int
	body   map[string]any
	bytes  []byte
}

func (s *suite) call(ctx context.Context, method, path string, payload any) (response, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return response{}, err
		}
		body = strings.NewReader(string(data))
	}
	return s.callRaw(ctx, method, path, body, payload != nil)
}

func (s *suite) callRaw(ctx context.Context, method, path string, body io.Reader, jsonBody bool) (resultValue response, resultErr error) {
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.cfg.gatewayURL, "/")+path, body)
	if err != nil {
		return response{}, err
	}
	if jsonBody {
		request.Header.Set("Content-Type", "application/json")
	}
	httpResult, err := s.client.Do(request)
	if err != nil {
		return response{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, httpResult.Body.Close()) }()
	data, err := io.ReadAll(io.LimitReader(httpResult.Body, (16<<20)+1))
	if err != nil {
		return response{}, err
	}
	if len(data) > 16<<20 {
		return response{}, errors.New("gateway response exceeded acceptance limit")
	}
	parsed := make(map[string]any)
	if len(data) > 0 {
		if err := json.Unmarshal(data, &parsed); err != nil {
			return response{status: httpResult.StatusCode, bytes: data}, nil
		}
	}
	return response{status: httpResult.StatusCode, body: parsed, bytes: data}, nil
}

func (s *suite) expect(method, path string, payload any, status int) (response, error) {
	result, err := s.call(context.Background(), method, path, payload)
	if err != nil {
		return response{}, err
	}
	if result.status != status {
		return result, fmt.Errorf("%s %s status=%d body=%s, want %d", method, path, result.status, result.bytes, status)
	}
	return result, nil
}

func (s *suite) jsonError(method, path, raw string, status int) (response, error) {
	result, err := s.callRaw(context.Background(), method, path, strings.NewReader(raw), true)
	if err != nil {
		return response{}, err
	}
	if result.status != status {
		return result, fmt.Errorf("%s %s status=%d body=%s, want %d", method, path, result.status, result.bytes, status)
	}
	return result, nil
}

func (s *suite) product(tag, productID string) error {
	_, err := s.expect(http.MethodPost, "/api/serving/products", map[string]any{"commandID": "register-" + tag, "productID": productID, "name": "Product " + tag, "baseUnit": "g"}, http.StatusOK)
	return err
}

func (s *suite) dish(tag, dishID, productID, quantity string) error {
	_, err := s.expect(http.MethodPost, "/api/serving/dishes", map[string]any{"commandID": "define-" + tag, "dishID": dishID, "name": "Dish " + tag, "ingredients": []any{map[string]any{"productID": productID, "quantity": quantity, "baseUnit": "g"}}}, http.StatusOK)
	return err
}

func (s *suite) delivery(tag, deliveryID, productID, quantity string, manufactured, received time.Time) (string, error) {
	request := map[string]any{"commandID": "receive-" + tag, "deliveryID": deliveryID, "number": "Delivery " + tag, "createdAt": moscow(received), "lines": []any{map[string]any{"productID": productID, "manufacturedAt": moscow(manufactured), "shelfLife": map[string]any{"value": "10", "unit": "day"}, "receivedAt": moscow(received), "quantity": quantity}}}
	requestBytes, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	hostSendAt := time.Now().In(time.FixedZone("MSK", 3*60*60)).Format(time.RFC3339Nano)
	result, err := s.callRaw(context.Background(), http.MethodPost, "/api/warehouse/deliveries", strings.NewReader(string(requestBytes)), true)
	if err == nil && result.status != http.StatusOK {
		err = fmt.Errorf("POST /api/warehouse/deliveries returned status %d body=%s, want %d", result.status, result.bytes, http.StatusOK)
	}
	if err != nil {
		warehouseTime, clockErr := s.warehouseEvaluatedAt()
		var clockDetail string
		if clockErr != nil {
			clockDetail = "unavailable: " + clockErr.Error()
		} else {
			clockDetail = warehouseTime.Format(time.RFC3339Nano)
		}
		return "", fmt.Errorf("%w; request=%s; hostSendAt=%s; warehouseEvaluatedAtAfterFailure=%s", err, requestBytes, hostSendAt, clockDetail)
	}
	batchIDs := array(result.body["batchIDs"])
	if len(batchIDs) != 1 {
		return "", fmt.Errorf("delivery %s returned %d batches", deliveryID, len(batchIDs))
	}
	return text(batchIDs[0]), nil
}

func (s *suite) proposal(dishID, portions string) (map[string]any, error) {
	positions := []any{}
	if dishID != "" {
		positions = []any{map[string]any{"dishID": dishID, "portions": portions}}
	}
	result, err := s.expect(http.MethodPost, "/api/serving/order-proposals", map[string]any{"positions": positions}, http.StatusOK)
	return result.body, err
}

func (s *suite) confirm(ctx context.Context, commandID, proposalID string) (response, error) {
	return s.call(ctx, http.MethodPost, "/api/serving/order-confirmations", map[string]any{"commandID": commandID, "proposalID": proposalID})
}

func (s *suite) resolve(commandID, proposalID string) (response, error) {
	return s.call(context.Background(), http.MethodPost, "/api/serving/order-confirmations/resolve", map[string]any{"commandID": commandID, "proposalID": proposalID})
}

func (s *suite) inventory() (map[string]any, error) {
	result, err := s.expect(http.MethodGet, "/api/warehouse/inventory", nil, http.StatusOK)
	return result.body, err
}

func (s *suite) history() (map[string]any, error) {
	result, err := s.expect(http.MethodGet, "/api/warehouse/history", nil, http.StatusOK)
	return result.body, err
}

func (s *suite) quantity(inventory map[string]any, productID string) string {
	for _, value := range array(inventory["products"]) {
		if text(object(value)["productID"]) == productID {
			return text(object(value)["quantity"])
		}
	}
	return "0"
}

func eventCount(history map[string]any, kind string) int {
	count := 0
	for _, value := range array(history["events"]) {
		if text(object(value)["type"]) == kind {
			count++
		}
	}
	return count
}

func eventCountForCommand(history map[string]any, kind, commandID string) int {
	count := 0
	for _, value := range array(history["events"]) {
		event := object(value)
		if text(event["type"]) == kind && text(event["commandID"]) == commandID {
			count++
		}
	}
	return count
}

func eventCountForProposal(history map[string]any, kind, proposalID string) int {
	count := 0
	for _, value := range array(history["events"]) {
		event := object(value)
		order := object(object(event["payload"])["order"])
		if text(event["type"]) == kind && text(order["proposalID"]) == proposalID {
			count++
		}
	}
	return count
}

func (s *suite) now() (time.Time, error) {
	evaluatedAt, err := s.warehouseEvaluatedAt()
	if err != nil {
		return time.Time{}, err
	}
	return evaluatedAt.Truncate(time.Second), nil
}

func (s *suite) warehouseEvaluatedAt() (time.Time, error) {
	inventory, err := s.inventory()
	if err != nil {
		return time.Time{}, fmt.Errorf("read warehouse clock from inventory: %w", err)
	}
	raw, ok := inventory["evaluatedAt"].(string)
	if !ok || raw == "" {
		return time.Time{}, errors.New("warehouse inventory omitted evaluatedAt")
	}
	evaluatedAt, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse warehouse inventory evaluatedAt %q: %w", raw, err)
	}
	_, offset := evaluatedAt.Zone()
	if offset != 3*60*60 {
		return time.Time{}, fmt.Errorf("warehouse inventory evaluatedAt %q does not use Moscow UTC+03:00", raw)
	}
	return evaluatedAt, nil
}
func moscow(value time.Time) string {
	return value.In(time.FixedZone("MSK", 3*60*60)).Format(time.RFC3339)
}
func array(value any) []any {
	result, ok := value.([]any)
	if !ok {
		return nil
	}
	return result
}

func object(value any) map[string]any {
	result, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return result
}

func text(value any) string {
	result, ok := value.(string)
	if !ok {
		return ""
	}
	return result
}
func id(value map[string]any, name string) (string, error) {
	result := text(value[name])
	if result == "" {
		return "", fmt.Errorf("response omitted %s", name)
	}
	return result, nil
}
func contains(values []any, key, expected string) bool {
	for _, value := range values {
		if text(object(value)[key]) == expected {
			return true
		}
	}
	return false
}

type restartConfig struct {
	factoryDir, project, override string
}

func restart(parent context.Context, cfg restartConfig, service string) error {
	if err := validateRestartConfig(cfg, service); err != nil {
		return err
	}
	args := []string{"compose", "--project-name", cfg.project, "--file", filepath.Join(cfg.factoryDir, "compose.yaml")}
	if cfg.override != "" {
		args = append(args, "--file", cfg.override)
	}
	args = append(args, "--env-file", filepath.Join(cfg.factoryDir, ".env.example"), "restart", "--timeout", "10", service)
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", args...)
	command.Dir = cfg.factoryDir
	command.Env = os.Environ()
	data, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("restart %s: %s: %w", service, strings.TrimSpace(string(data)), err)
	}
	return nil
}

func validateRestartConfig(cfg restartConfig, service string) error {
	if !filepath.IsAbs(cfg.factoryDir) {
		return errors.New("factory directory must be absolute")
	}
	if !ownedComposeProject(cfg.project) {
		return fmt.Errorf("compose project %q is not an owned HTTP acceptance project", cfg.project)
	}
	if service != "warehouse" && service != "serving" {
		return fmt.Errorf("service %q is not restartable by acceptance scenarios", service)
	}
	for _, name := range []string{"compose.yaml", ".env.example"} {
		path := filepath.Join(cfg.factoryDir, name)
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("required factory Compose file %s: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("required factory Compose file %s is not a regular file", name)
		}
	}
	if cfg.override != "" {
		if !filepath.IsAbs(cfg.override) {
			return errors.New("compose override must be absolute")
		}
		info, err := os.Stat(cfg.override)
		if err != nil {
			return fmt.Errorf("compose override is unavailable: %w", err)
		}
		if !info.Mode().IsRegular() {
			return errors.New("compose override is not a regular file")
		}
	}
	return nil
}

func ownedComposeProject(project string) bool {
	base := strings.TrimSuffix(project, "-fault")
	const prefix = "umir-http-"
	if !strings.HasPrefix(base, prefix) {
		return false
	}
	suffix := strings.TrimPrefix(base, prefix)
	if len(suffix) != 24 {
		return false
	}
	for _, char := range suffix {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}

func composeProjectMatchesPhase(project, phase string) bool {
	if !ownedComposeProject(project) {
		return false
	}
	switch phase {
	case "normal":
		return !strings.HasSuffix(project, "-fault")
	case "faults":
		return strings.HasSuffix(project, "-fault")
	default:
		return false
	}
}

func waitHealth(client *http.Client, endpoint string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for ctx.Err() == nil {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			data, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
			closeErr := response.Body.Close()
			if closeErr != nil {
				return fmt.Errorf("close health response: %w", closeErr)
			}
			if readErr == nil && response.StatusCode == http.StatusOK && strings.Contains(string(data), `"status":"ok"`) {
				return nil
			}
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return fmt.Errorf("health did not recover within %s: %s", timeout, endpoint)
}

type faultRule struct {
	ID              string `json:"id"`
	Method          string `json:"method,omitempty"`
	Path            string `json:"path"`
	Action          string `json:"action"`
	MaxUses         int    `json:"maxUses"`
	Timeout         int64  `json:"timeout,omitempty"`
	DetachOnRelease bool   `json:"detachOnRelease,omitempty"`
	DetachedTimeout int64  `json:"detachedTimeout,omitempty"`
}

func (c *faultControl) arm(ctx context.Context, rule faultRule) error {
	return c.do(ctx, http.MethodPost, "/control/rules", rule, nil)
}
func (c *faultControl) release(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/control/release?id="+url.QueryEscape(id), nil, nil)
}
func (c *faultControl) wait(ctx context.Context, id, event string, timeout time.Duration) (map[string]any, error) {
	var result map[string]any
	path := "/control/events?id=" + url.QueryEscape(id) + "&event=" + url.QueryEscape(event) + "&waitMs=" + strconv.FormatInt(timeout.Milliseconds(), 10)
	err := c.do(ctx, http.MethodGet, path, nil, &result)
	return result, err
}

func (c *faultControl) stats(ctx context.Context, method, path string) (proxyTraffic, error) {
	var result proxyTraffic
	query := url.Values{}
	query.Set("method", method)
	query.Set("path", path)
	err := c.do(ctx, http.MethodGet, "/control/stats?"+query.Encode(), nil, &result)
	return result, err
}

func (c *faultControl) resetStats(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/control/reset-stats", nil, nil)
}
func (c *faultControl) do(ctx context.Context, method, path string, input, output any) (resultErr error) {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(data))
	}
	request, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, response.Body.Close()) }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("fault control %s returned %d: %s", path, response.StatusCode, data)
	}
	if output != nil {
		return json.Unmarshal(data, output)
	}
	return nil
}
