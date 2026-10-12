package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Operator auth acceptance (gateway#8): the harness signs in through the public frontend URL and sends
// every business request as the browser does.
const (
	csrfHeader     = "X-CSRF-Token"
	sessionCookie  = "umir_operator_session"
	preauthCookie  = "umir_operator_preauth"
	maxPasswordLen = 4096
)

// operatorSession is a signed-in operator. Every request carries its cookie; unsafe requests also carry
// the exact public Origin and the session's CSRF token.
type operatorSession struct {
	cookie *http.Cookie
	csrf   string
	origin string
}

func unsafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

func (o *operatorSession) apply(request *http.Request) {
	if o == nil {
		return
	}
	request.AddCookie(o.cookie)
	if unsafeMethod(request.Method) {
		request.Header.Set("Origin", o.origin)
		request.Header.Set(csrfHeader, o.csrf)
	}
}

// rawHeaders returns the same credentials as header lines for hand-written requests.
func (o *operatorSession) rawHeaders(method string) string {
	if o == nil {
		return ""
	}
	lines := "Cookie: " + o.cookie.Name + "=" + o.cookie.Value + "\r\n"
	if unsafeMethod(method) {
		lines += "Origin: " + o.origin + "\r\n" + csrfHeader + ": " + o.csrf + "\r\n"
	}
	return lines
}

// readPassword reads the disposable operator password; one trailing newline is not part of it.
func readPassword(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("operator password file must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("operator password file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxPasswordLen {
		return "", errors.New("operator password file must be a small regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("operator password file: %w", err)
	}
	password := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if password == "" {
		return "", errors.New("operator password file is empty")
	}
	return password, nil
}

func (s *suite) origin() string { return strings.TrimRight(s.cfg.gatewayURL, "/") }

// signIn takes a CSRF token for an anonymous context and logs in with it, as the frontend does.
func (s *suite) signIn(password string) (*operatorSession, response, error) {
	bootstrap, err := s.exchange(http.MethodGet, "/api/operator/csrf", "", nil, nil)
	if err != nil {
		return nil, bootstrap, err
	}
	preauth := cookieNamed(bootstrap.cookies, preauthCookie)
	token := text(bootstrap.body["csrfToken"])
	if bootstrap.status != http.StatusOK || preauth == nil || token == "" {
		return nil, bootstrap, fmt.Errorf("CSRF bootstrap returned %d %s without a pre-auth cookie and token", bootstrap.status, bootstrap.bytes)
	}
	credentials, err := jsonMarshal(map[string]string{"login": s.cfg.operatorLogin, "password": password})
	if err != nil {
		return nil, response{}, err
	}
	login, err := s.exchange(http.MethodPost, "/api/operator/session", credentials, map[string]string{"Origin": s.origin(), csrfHeader: token}, []*http.Cookie{preauth})
	if err != nil {
		return nil, login, err
	}
	session := cookieNamed(login.cookies, sessionCookie)
	csrf := text(object(login.body["session"])["csrfToken"])
	if login.status != http.StatusOK || session == nil || csrf == "" {
		return nil, login, fmt.Errorf("login returned %d %s without a session cookie and token", login.status, login.bytes)
	}
	return &operatorSession{cookie: &http.Cookie{Name: session.Name, Value: session.Value}, csrf: csrf, origin: s.origin()}, login, nil
}

// exchange sends one request with exactly the given headers and cookies and no operator session.
func (s *suite) exchange(method, path, body string, headers map[string]string, cookies []*http.Cookie) (response, error) {
	request, err := http.NewRequestWithContext(context.Background(), method, s.origin()+path, strings.NewReader(body))
	if err != nil {
		return response{}, err
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	return s.send(request)
}

func cookieNamed(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func jsonMarshal(value any) (string, error) {
	data, err := json.Marshal(value)
	return string(data), err
}

func problemCode(result response) string { return text(result.body["code"]) }

// runAuth signs in, records A01–A04 and leaves the suite signed in for the business scenarios.
func (s *suite) runAuth() error {
	password, err := readPassword(s.cfg.operatorPasswordFile)
	if err != nil {
		return err
	}
	scenarios := []struct {
		id  string
		run func(string) error
	}{{"A02", s.a02}, {"A01", s.a01}, {"A03", s.a03}, {"A04", s.a04}}
	for _, scenario := range scenarios {
		s.current = scenario.id
		if err := scenario.run(password); err != nil {
			return err
		}
	}
	return nil
}

// a02 signs in through the frontend and reads the operator's own view.
func (s *suite) a02(password string) error {
	bootstrap, err := s.exchange(http.MethodGet, "/api/operator/csrf", "", nil, nil)
	if err != nil {
		return err
	}
	preauth := cookieNamed(bootstrap.cookies, preauthCookie)
	if preauth == nil || !preauth.HttpOnly || preauth.SameSite != http.SameSiteStrictMode || preauth.Path != "/" || preauth.Secure {
		return fmt.Errorf("pre-auth cookie %v is not HttpOnly, SameSite=Strict and Path=/ without Secure in loopback mode", preauth)
	}
	wrongCredentials, err := jsonMarshal(map[string]string{"login": s.cfg.operatorLogin, "password": password + "-wrong"})
	if err != nil {
		return err
	}
	rejected, err := s.exchange(http.MethodPost, "/api/operator/session", wrongCredentials, map[string]string{"Origin": s.origin(), csrfHeader: text(bootstrap.body["csrfToken"])}, []*http.Cookie{preauth})
	if err != nil {
		return err
	}
	if rejected.status != http.StatusUnauthorized || problemCode(rejected) != "InvalidCredentials" {
		return fmt.Errorf("wrong password returned %d %s, want 401 InvalidCredentials", rejected.status, rejected.bytes)
	}
	session, login, err := s.signIn(password)
	if err != nil {
		return err
	}
	cookie := cookieNamed(login.cookies, sessionCookie)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Secure {
		return fmt.Errorf("session cookie %v is not HttpOnly, SameSite=Strict and Path=/ without Secure in loopback mode", cookie)
	}
	view := object(login.body["session"])
	expires, err := time.Parse(time.RFC3339, text(view["expiresAt"]))
	if err != nil || !strings.HasSuffix(text(view["expiresAt"]), "+03:00") || view["rememberMe"] != false {
		return fmt.Errorf("login session %v is not an ordinary session with a +03:00 deadline", view)
	}
	s.session = session
	me, err := s.expect(http.MethodGet, "/api/operator/me", nil, http.StatusOK)
	if err != nil {
		return err
	}
	if text(object(me.body["operator"])["login"]) != s.cfg.operatorLogin {
		return fmt.Errorf("/me returned %s for another operator", me.bytes)
	}
	s.record("A02", map[string]string{"login": "200", "wrongPassword": "401 InvalidCredentials", "me": "200", "expiresAt": expires.Format(time.RFC3339)})
	return nil
}

// a01 proves that business routes refuse a request without the operator session before dispatch.
func (s *suite) a01(string) error {
	product := map[string]any{"commandID": "register-A01", "productID": "product-A01", "name": "Product A01", "baseUnit": "g"}
	observations := make(map[string]string, 3)
	for _, call := range []struct {
		method, path string
		payload      any
	}{
		{http.MethodGet, "/api/warehouse/inventory", nil},
		{http.MethodGet, "/api/serving/menu", nil},
		{http.MethodPost, "/api/serving/products", product},
	} {
		body := ""
		if call.payload != nil {
			data, err := jsonMarshal(call.payload)
			if err != nil {
				return err
			}
			body = data
		}
		result, err := s.exchange(call.method, call.path, body, map[string]string{"Origin": s.origin()}, nil)
		if err != nil {
			return err
		}
		if result.status >= 200 && result.status < 300 {
			return fmt.Errorf("unauthenticated %s %s succeeded with %d", call.method, call.path, result.status)
		}
		if result.status != http.StatusUnauthorized || problemCode(result) != "Unauthenticated" || result.header.Get("WWW-Authenticate") == "" {
			return fmt.Errorf("unauthenticated %s %s returned %d %s, want 401 Unauthenticated with a challenge", call.method, call.path, result.status, result.bytes)
		}
		observations[call.method+" "+call.path] = "401"
	}
	if err := s.absentProduct("product-A01"); err != nil {
		return err
	}
	s.record("A01", observations)
	return nil
}

// a03 proves that an unsafe business request needs the exact Origin and the session's CSRF token.
func (s *suite) a03(string) error {
	product, err := jsonMarshal(map[string]any{"commandID": "register-A03", "productID": "product-A03", "name": "Product A03", "baseUnit": "g"})
	if err != nil {
		return err
	}
	cookies := []*http.Cookie{s.session.cookie}
	for name, call := range map[string]struct {
		headers map[string]string
		code    string
	}{
		"no token":       {map[string]string{"Origin": s.origin()}, "InvalidCsrfToken"},
		"wrong token":    {map[string]string{"Origin": s.origin(), csrfHeader: "forged"}, "InvalidCsrfToken"},
		"foreign origin": {map[string]string{"Origin": "http://evil.test", csrfHeader: s.session.csrf}, "InvalidOrigin"},
		"cross-site":     {map[string]string{"Origin": s.origin(), csrfHeader: s.session.csrf, "Sec-Fetch-Site": "cross-site"}, "InvalidOrigin"},
	} {
		result, err := s.exchange(http.MethodPost, "/api/serving/products", product, call.headers, cookies)
		if err != nil {
			return err
		}
		if result.status != http.StatusForbidden || problemCode(result) != call.code {
			return fmt.Errorf("%s returned %d %s, want 403 %s", name, result.status, result.bytes, call.code)
		}
	}
	if err := s.absentProduct("product-A03"); err != nil {
		return err
	}
	s.record("A03", map[string]string{"missingOrWrongToken": "403 InvalidCsrfToken", "foreignOrCrossSiteOrigin": "403 InvalidOrigin"})
	return nil
}

// a04 logs a second session out and proves that only that session ended.
func (s *suite) a04(password string) error {
	second, _, err := s.signIn(password)
	if err != nil {
		return err
	}
	logout, err := s.exchange(http.MethodDelete, "/api/operator/session", "", map[string]string{"Origin": s.origin(), csrfHeader: second.csrf}, []*http.Cookie{second.cookie})
	if err != nil {
		return err
	}
	cleared := cookieNamed(logout.cookies, sessionCookie)
	if logout.status != http.StatusNoContent || cleared == nil || cleared.MaxAge >= 0 {
		return fmt.Errorf("logout returned %d %v without clearing the session cookie", logout.status, logout.header.Values("Set-Cookie"))
	}
	revoked, err := s.exchange(http.MethodGet, "/api/operator/me", "", nil, []*http.Cookie{second.cookie})
	if err != nil {
		return err
	}
	if revoked.status != http.StatusUnauthorized {
		return fmt.Errorf("logged-out session read /me with %d", revoked.status)
	}
	if _, err := s.expect(http.MethodGet, "/api/operator/me", nil, http.StatusOK); err != nil {
		return fmt.Errorf("logout of another session ended this one: %w", err)
	}
	s.record("A04", map[string]string{"logout": "204", "loggedOutMe": "401", "otherSession": "200"})
	return nil
}

// signInForScenarios signs in for the fault scenarios, which record no auth outcomes.
func (s *suite) signInForScenarios() error {
	password, err := readPassword(s.cfg.operatorPasswordFile)
	if err != nil {
		return err
	}
	session, _, err := s.signIn(password)
	if err != nil {
		return err
	}
	s.session = session
	return nil
}

func (s *suite) absentProduct(productID string) error {
	products, err := s.expect(http.MethodGet, "/api/serving/products", nil, http.StatusOK)
	if err != nil {
		return err
	}
	if contains(array(products.body["items"]), "productID", productID) {
		return fmt.Errorf("a rejected request created %s", productID)
	}
	return nil
}
