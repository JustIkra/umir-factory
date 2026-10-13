package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JustIkra/umir-factory/tests/http/internal/faultproxy"
)

//go:embed backend-public.json
var backendWire embed.FS

type backendPublicOperation struct {
	Path, Method, ID string
	NeedsJWT         bool
	Request          json.RawMessage
	Responses        map[string]json.RawMessage
}
type backendSession struct {
	base, origin, csrf string
	client             *backendPublicClient
	wire               []byte
	operations         map[string]backendPublicOperation
	observations       []backendHTTPObservation
	lastReply          backendPublicReply
	probe              func(context.Context) (func(), error)
	mu                 sync.Mutex
}
type backendHTTPObservation struct {
	Operation     string `json:"operation"`
	Method        string `json:"method"`
	Route         string `json:"route"`
	Status        int    `json:"status"`
	RequestValid  bool   `json:"requestValid"`
	ResponseValid bool   `json:"responseValid"`
}

func newBackendSession(base, origin string) *backendSession {
	jar, e := cookiejar.New(nil)
	if e != nil {
		return nil
	}
	raw, e := backendWire.ReadFile("backend-public.json")
	if e != nil || faultproxy.SHA256(raw) != "e710150288d650965cfee80b7cb623f5466583c03da9edf5561f94d127970897" {
		return nil
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	operations := map[string]backendPublicOperation{}
	for path, methods := range doc.Paths {
		for method, value := range methods {
			if !slices.Contains([]string{"get", "post", "put", "patch", "delete", "head", "options", "trace"}, method) {
				continue
			}
			var op struct {
				OperationID string                     `json:"operationId"`
				RequestBody json.RawMessage            `json:"requestBody"`
				Responses   map[string]json.RawMessage `json:"responses"`
			}
			if json.Unmarshal(value, &op) != nil || op.OperationID == "" {
				return nil
			}
			operations[op.OperationID] = backendPublicOperation{Path: path, Method: strings.ToUpper(method), ID: op.OperationID, Request: op.RequestBody, Responses: op.Responses, NeedsJWT: false}
		}
	}
	endpoint, err := url.Parse(base)
	if err != nil || !canonicalBackendURL(base) {
		return nil
	}
	session := &backendSession{base: base, origin: origin, wire: raw, operations: operations}
	session.client = &backendPublicClient{Jar: jar, Transport: backendProbeTransport(endpoint.Host), session: session}
	return session
}
func schemaPointer(path, method string, request bool, status int) string {
	escaped := strings.ReplaceAll(strings.ReplaceAll(path, "~", "~0"), "/", "~1")
	pointer := "#/paths/" + escaped + "/" + strings.ToLower(method)
	if request {
		return pointer + "/requestBody/content/application~1json/schema"
	}
	return pointer + "/responses/" + strconv.Itoa(status) + "/content/application~1json/schema"
}
func (b *backendSession) call(ctx context.Context, opID string, params map[string]string, payload any, headers map[string]string, status int) (map[string]any, error) {
	return b.callQuery(ctx, opID, params, nil, payload, headers, status)
}
func (b *backendSession) callQuery(ctx context.Context, opID string, params map[string]string, query url.Values, payload any, headers map[string]string, status int) (map[string]any, error) {
	var raw []byte
	if payload != nil {
		var err error
		raw, err = json.Marshal(payload)
		if err != nil {
			return nil, faultproxy.ErrProtocol
		}
	}
	return b.callBytes(ctx, opID, params, query, raw, headers, true, status)
}

type backendPublicReply struct {
	Status int
	Body   json.RawMessage
	Header http.Header
}

func (b *backendSession) callBytes(ctx context.Context, opID string, params map[string]string, query url.Values, raw []byte, headers map[string]string, strictRequest bool, status int) (map[string]any, error) {
	if b == nil {
		return nil, errors.New("public binding unavailable")
	}
	op, ok := b.operations[opID]
	if !ok {
		return nil, errors.New("unsupported public operation")
	}
	route := op.Path
	for name, value := range params {
		route = strings.ReplaceAll(route, "{"+name+"}", url.PathEscape(value))
	}
	if strings.ContainsAny(route, "{}") {
		return nil, faultproxy.ErrProtocol
	}
	if len(raw) > 1<<20 {
		return nil, faultproxy.ErrProtocol
	}
	requestValid := true
	if len(op.Request) > 0 {
		requestValid = faultproxy.ValidateSchema(b.wire, schemaPointer(op.Path, op.Method, true, 0), raw, 32) == nil
	} else if len(raw) > 0 {
		requestValid = false
	}
	if strictRequest && !requestValid {
		return nil, errors.New("request schema rejected")
	}
	if len(query) > 0 {
		route += "?" + query.Encode()
	}

	req, e := http.NewRequestWithContext(ctx, op.Method, b.base+route, bytes.NewReader(raw))
	if e != nil {
		return nil, faultproxy.ErrProtocol
	}
	if raw != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if op.Method != http.MethodGet {
		req.Header.Set("Origin", b.origin)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("X-CSRF-Token", b.csrfValue())
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	res, e := b.client.Do(req)
	if e != nil {
		return nil, errors.New("public transport outcome unknown")
	}
	body, e := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	e = errors.Join(e, res.Body.Close())
	if e != nil || len(body) > 1<<20 {
		return nil, errors.New("public response unavailable")
	}
	b.mu.Lock()
	b.lastReply = backendPublicReply{res.StatusCode, slices.Clone(body), res.Header.Clone()}
	b.mu.Unlock()
	observation := backendHTTPObservation{Operation: opID, Method: op.Method, Route: op.Path, Status: res.StatusCode, RequestValid: requestValid}
	defer func() { b.mu.Lock(); b.observations = append(b.observations, observation); b.mu.Unlock() }()
	if res.StatusCode != status {
		return nil, fmt.Errorf("public operation %s status=%d expected=%d", opID, res.StatusCode, status)
	}
	pointer := schemaPointer(op.Path, op.Method, false, status)
	if _, ok := op.Responses[strconv.Itoa(status)]; !ok {
		pointer = strings.ReplaceAll(pointer, "/responses/"+strconv.Itoa(status)+"/", "/responses/default/")
	}
	if len(body) == 0 {
		if status != 204 {
			return nil, errors.New("typed public response missing")
		}
		observation.ResponseValid = true
		return map[string]any{}, nil
	}
	if faultproxy.ValidateSchema(b.wire, pointer, body, 32) != nil {
		return nil, errors.New("public response schema rejected")
	}
	parsed, e := faultproxy.StrictJSON(body, 32)
	data, ok := parsed.(map[string]any)
	if e != nil || !ok {
		return nil, faultproxy.ErrProtocol
	}
	observation.ResponseValid = true
	return data, nil
}
func (b *backendSession) csrfValue() string { b.mu.Lock(); defer b.mu.Unlock(); return b.csrf }
func (b *backendSession) publicReply() backendPublicReply {
	b.mu.Lock()
	defer b.mu.Unlock()
	return backendPublicReply{b.lastReply.Status, slices.Clone(b.lastReply.Body), b.lastReply.Header.Clone()}
}

func (b *backendSession) csrfToken(ctx context.Context) error {
	data, e := b.call(ctx, "csrf", nil, nil, nil, 200)
	if e != nil {
		return e
	}
	csrf, ok := data["csrfToken"].(string)
	if !ok || csrf == "" {
		return faultproxy.ErrProtocol
	}
	b.mu.Lock()
	b.csrf = csrf
	b.mu.Unlock()
	return nil
}
func (b *backendSession) login(ctx context.Context, password []byte) error {
	user, e := b.loginAs(ctx, "e2e.initial", password, "fixture-login-1", false)
	if e != nil {
		return e
	}
	if user["userID"] != "11111111-1111-4111-8111-111111111111" || user["login"] != "e2e.initial" {
		return errors.New("bootstrap profile differs")
	}
	return b.csrfToken(ctx)
}

func (b *backendSession) loginAs(ctx context.Context, login string, password []byte, command string, remember bool) (map[string]any, error) {
	if err := b.csrfToken(ctx); err != nil {
		return nil, err
	}
	user, err := b.call(ctx, "login", nil, map[string]any{"login": login, "password": string(password), "rememberMe": remember, "commandID": command}, nil, 200)
	if err != nil {
		return nil, err
	}
	if err := b.csrfToken(ctx); err != nil {
		return nil, err
	}
	return user, nil
}

type backendScope struct{ OrgID, RestaurantID string }

func (b *backendSession) organization(ctx context.Context, index int, zone string) (backendScope, error) {
	command, err := faultproxy.NewUUID()
	if err != nil {
		return backendScope{}, err
	}
	data, e := b.call(ctx, "createOrganization", nil, map[string]any{"commandID": "fixture-org-" + command, "name": fmt.Sprintf("Fixture Organization %d", index), "timeZone": zone, "firstRestaurantName": "Fixture Restaurant"}, nil, 201)
	if e != nil {
		return backendScope{}, e
	}
	org, ok := data["orgID"].(string)
	if !ok || !faultproxy.UUID(org) {
		return backendScope{}, faultproxy.ErrProtocol
	}
	restaurants, ok := data["restaurantIDs"].([]any)
	if !ok || len(restaurants) != 1 {
		return backendScope{}, faultproxy.ErrProtocol
	}
	restaurant, ok := restaurants[0].(string)
	if !ok || !faultproxy.UUID(restaurant) {
		return backendScope{}, faultproxy.ErrProtocol
	}
	operation, ok := data["operationID"].(string)
	if !ok {
		return backendScope{}, faultproxy.ErrProtocol
	}
	if e := b.waitLifecycle(ctx, org, operation); e != nil {
		return backendScope{}, e
	}
	return backendScope{org, restaurant}, nil
}
func (b *backendSession) waitLifecycle(ctx context.Context, org, operation string) error {
	for {
		data, e := b.call(ctx, "lifecycle", map[string]string{"orgID": org, "operationID": operation}, nil, nil, 200)
		if e != nil {
			return e
		}
		if data["lifecycle"] == "ready" || data["lifecycle"] == "active" {
			return nil
		}
		services, ok := data["services"].([]any)
		if !ok {
			return faultproxy.ErrProtocol
		}
		ready := len(services) >= 3
		for _, s := range services {
			m, ok := s.(map[string]any)
			if !ok || m["state"] != "ready" {
				ready = false
			}
		}
		if ready {
			return nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.New("lifecycle wait expired")
		case <-timer.C:
		}
	}
}
func (b *backendSession) enroll(ctx context.Context, client *backendClient, scope backendScope) error {
	id, err := faultproxy.NewUUID()
	if err != nil {
		return err
	}
	_, err = b.enrollUser(ctx, client, scope, "fixture-"+id+"@umir.invalid", []string{"Warehouse", "Waiter"})
	return err
}
func (b *backendSession) enrollUser(ctx context.Context, client *backendClient, scope backendScope, email string, roles []string) (*backendSession, error) {
	command, e := faultproxy.NewUUID()
	if e != nil {
		return nil, e
	}
	if !strings.HasSuffix(email, "@umir.invalid") && !strings.HasSuffix(email, "@example.invalid") {
		return nil, errors.New("synthetic recipient required")
	}

	invitation, e := b.call(ctx, "createInvitation", map[string]string{"orgID": scope.OrgID}, map[string]any{"commandID": "fixture-invite-user-" + command, "email": email, "assignments": []any{map[string]any{"restaurantID": scope.RestaurantID, "roles": roles}}}, nil, 201)
	if e != nil {
		return nil, e
	}
	id, ok := invitation["invitationID"].(string)
	if !ok {
		return nil, faultproxy.ErrProtocol
	}
	invited := newBackendSession(b.base, b.origin)
	invited.probe = b.probe
	invited.client.Transport = b.client.Transport
	if e := invited.csrfToken(ctx); e != nil {
		return nil, e
	}
	challenge, e := invited.call(ctx, "beginInvitationChallenge", map[string]string{"invitationID": id}, map[string]any{"commandID": "fixture-invite-begin-" + command, "invitationID": id}, nil, 200)
	if e != nil {
		return nil, e
	}
	challengeID, ok := challenge["challengeID"].(string)
	if !ok {
		return nil, faultproxy.ErrProtocol
	}
	expiry, ok := challenge["expiresAt"].(string)
	if !ok {
		return nil, faultproxy.ErrProtocol
	}
	selector := map[string]any{"kind": "command", "challengeID": challengeID, "commandID": "fixture-invite-begin-" + command, "operationID": "beginInvitationChallenge"}
	projection, e := client.discoverJob(ctx, selector, expiry)
	if e != nil {
		return nil, e
	}
	jobs, ok := projection["jobs"].([]any)
	if !ok || len(jobs) != 1 {
		return nil, errors.New("CorrelationConflict")
	}
	job, ok := jobs[0].(map[string]any)
	if !ok {
		return nil, faultproxy.ErrProtocol
	}
	clock, e := client.clock(ctx, "auth")
	if e != nil {
		return nil, e
	}
	code, e := client.otp(ctx, job, clock)
	if e != nil {
		return nil, e
	}
	verified, e := invited.call(ctx, "verifyChallenge", map[string]string{"challengeID": challengeID}, map[string]any{"commandID": "fixture-invite-verify-" + command, "challengeID": challengeID, "code": code}, nil, 200)

	if e != nil {
		return nil, e
	}
	proof, ok := verified["proof"].(string)
	if !ok {
		return nil, faultproxy.ErrProtocol
	}
	password := make([]byte, 16)
	if _, e := rand.Read(password); e != nil {
		return nil, errors.New("private entropy unavailable")
	}
	_, e = invited.call(ctx, "completeInvitedUser", map[string]string{"challengeID": challengeID}, map[string]any{"commandID": "fixture-invite-complete-" + command, "challengeID": challengeID, "proof": proof, "password": hex.EncodeToString(password), "displayName": "Fixture Invited Operator"}, nil, 200)
	if e != nil {
		return nil, e
	}
	if e := invited.csrfToken(ctx); e != nil {
		return nil, e
	}
	return invited, nil
}

type backendPhaseResult struct {
	Status           string   `json:"status"`
	ContractIdentity string   `json:"contractIdentity"`
	Passed           []string `json:"passed"`
}

func writeBackendPhaseResult(path string, result backendPhaseResult) error {
	if !filepath.IsAbs(path) {
		return faultproxy.ErrProtocol
	}
	if info, e := os.Lstat(path); e == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0o600) {
		return faultproxy.ErrProtocol
	}
	raw, e := json.Marshal(result)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".backend-result-")
	if e != nil {
		return e
	}
	temp := f.Name()
	if _, e := f.Write(raw); e != nil {
		closeErr := f.Close()
		removeErr := os.Remove(temp)
		return errors.Join(e, closeErr, removeErr)
	}
	if e := f.Close(); e != nil {
		removeErr := os.Remove(temp)
		return errors.Join(e, removeErr)
	}
	if e := os.Rename(temp, path); e != nil {
		removeErr := os.Remove(temp)
		return errors.Join(e, removeErr)
	}
	return nil
}
func runBackendPhase(args []string, stdout, stderr io.Writer) (code int) {
	flags := flag.NewFlagSet("backend-acceptance", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	runPath := flags.String("backend-run", "", "private runner descriptor")
	resultPath := flags.String("backend-result", "", "private result")
	if flags.Parse(args) != nil || flags.NArg() != 0 || !filepath.IsAbs(*runPath) || !filepath.IsAbs(*resultPath) || filepath.Dir(*runPath) != filepath.Dir(*resultPath) {
		diagnostic(stderr, "invalid backend invocation\n")
		return 2
	}
	result := backendPhaseResult{Status: "incomplete", ContractIdentity: backendContract, Passed: []string{}}
	var session *backendSession
	var fixtureClient *backendClient
	defer func() {
		observations := map[string]any{"status": result.Status, "passed": result.Passed}
		if session != nil {
			observations["http"] = session.observations
		}
		if fixtureClient != nil {
			observations["privateHTTP"] = fixtureClient.privateObservations
			observations["controls"] = fixtureClient.observations
			observations["restarts"] = fixtureClient.restarts
			observations["feedback"] = fixtureClient.feedbackEvidence
		}
		raw, err := json.Marshal(observations)
		if err == nil {
			err = os.WriteFile(filepath.Join(filepath.Dir(*resultPath), "backend-observations.json"), raw, 0o600)
		}
		if err == nil {
			err = writeBackendPhaseResult(*resultPath, result)
		}
		if err != nil {
			code = 1
			result.Status = "incomplete"
			result.Passed = []string{}
			if writeErr := writeBackendPhaseResult(*resultPath, result); writeErr != nil {
				diagnostic(stderr, "backend evidence unavailable\n")
			}
		}
	}()

	if e := writeBackendPhaseResult(*resultPath, result); e != nil {
		diagnostic(stderr, "private backend result unavailable\n")
		return 1
	}
	d, e := readBackendRun(*runPath)
	if e != nil {
		diagnostic(stderr, "backend descriptor rejected\n")
		return 1
	}
	for _, id := range d.Requirements {
		if _, err := backendScenarioGroup(id); err != nil {
			diagnostic(stderr, "backend assertion %s has no complete execution binding\n", id)
			return 1
		}
	}
	c := newBackendClient(d)
	fixtureClient = c
	ctx, cancel := backendScenarioContext(context.Background())
	defer cancel()
	for _, owner := range backendTargets {
		if _, e := c.control(ctx, owner, "fixture.ready", map[string]any{}); e != nil {
			diagnostic(stderr, "fixture %s readiness unavailable\n", owner)
			return 1
		}
	}
	for _, owner := range backendOwners {
		clock, e := c.clock(ctx, owner)
		if e != nil || clock.Instant != d.Fixtures.InitialInstant || clock.Generation != d.Fixtures.InitialGeneration {
			diagnostic(stderr, "authoritative %s clock unavailable\n", owner)
			return 1
		}
	}
	bootstrap, e := c.control(ctx, "auth", "bootstrap.read", map[string]any{})
	if e != nil || bootstrap["initialized"] != true || bootstrap["profileBindingSHA256"] != d.Fixtures.BootstrapBindingSHA256 {
		diagnostic(stderr, "real bootstrap binding unavailable\n")
		return 1
	}
	b := newBackendSession(d.GatewayURL, d.Origin)
	if b == nil {
		return 1
	}
	qualifiedURL, parseErr := url.Parse(d.GatewayURL)
	if parseErr != nil {
		return 1
	}
	b.client.Transport = backendProbeTransport(qualifiedURL.Host)
	session = b
	b.probe = c.publicProbe
	password, e := c.credential("bootstrap-password")
	defer clear(password)
	if e != nil || b.login(ctx, password) != nil {
		diagnostic(stderr, "real initial login failed\n")
		return 1
	}
	clear(password)
	scopes := []backendScope{}
	for i, zone := range []string{"Europe/Moscow", "America/New_York"} {
		scope, e := b.organization(ctx, i, zone)
		if e != nil {
			diagnostic(stderr, "real organization provisioning failed\n")
			return 1
		}
		scopes = append(scopes, scope)
	}
	if e := b.enroll(ctx, c, scopes[0]); e != nil {
		diagnostic(stderr, "real invitation/OTP completion failed\n")
		return 1
	}
	cancel()
	for _, id := range d.Requirements {
		caseCtx, stop := backendScenarioContext(context.Background())
		caseScopes, err := b.freshScopes(caseCtx, []string{"Europe/Moscow", "America/New_York"})
		if err == nil {
			err = dispatchBackendScenario(caseCtx, backendScenarioInput{b, c, caseScopes}, id)
		}
		stop()
		if err != nil {
			diagnostic(stderr, "backend assertion %s failed or unimplemented\n", id)
			return 1
		}
		result.Passed = append(result.Passed, id)
	}

	result.Status = "ready"
	if e := writeBackendPhaseResult(*resultPath, result); e != nil {
		diagnostic(stderr, "backend result write failed\n")
		return 1
	}
	if _, e := io.WriteString(stdout, "backend assertions completed\n"); e != nil {
		return 1
	}
	return 0
}

func (b *backendSession) validateRefusal(raw []byte) error {
	if b == nil {
		return errors.New("public binding unavailable")
	}
	if faultproxy.ValidateSchema(b.wire, "#/components/schemas/Error", raw, 32) != nil {
		return errors.New("typed refusal missing")
	}
	value, e := faultproxy.StrictJSON(raw, 32)
	data, ok := value.(map[string]any)
	if e != nil || !ok || data["outcome"] != "not_committed" || !slices.Contains([]string{"InvalidArgument", "InvalidRequest"}, text(data["code"])) {
		return errors.New("definite validation refusal missing")
	}
	return nil
}
