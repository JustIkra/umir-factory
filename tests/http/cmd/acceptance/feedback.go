package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const feedbackBodyLimit = 16 << 10

type feedbackContext struct {
	cookies []*http.Cookie
	csrf    string
}

type feedbackAcceptance struct {
	suite       *suite
	anonymous   feedbackContext
	anonymousID string
	session     feedbackContext
	sessionID   string
	sessionBody map[string]any
}

func (s *suite) runFeedback() error {
	if s.session == nil || s.session.cookie == nil {
		return errors.New("feedback acceptance requires an authenticated operator session for the session-bound public checks")
	}
	acceptance := &feedbackAcceptance{suite: s}
	for _, scenario := range []struct {
		id  string
		run func() error
	}{
		{"F01", acceptance.f01},
		{"F02", acceptance.f02},
		{"F03", acceptance.f03},
		{"F04", acceptance.f04},
		{"F05", acceptance.f05},
	} {
		s.current = scenario.id
		if err := scenario.run(); err != nil {
			return fmt.Errorf("%s: %w", scenario.id, err)
		}
	}
	return nil
}

func (a *feedbackAcceptance) f01() error {
	anonymous, err := a.bootstrap(nil)
	if err != nil {
		return err
	}
	a.anonymous = anonymous
	anonymousID, err := newFeedbackSubmissionID()
	if err != nil {
		return err
	}
	a.anonymousID = anonymousID
	anonymousBody := feedbackPayload(anonymousID)
	created, err := a.submit(anonymous, anonymousBody)
	if err != nil {
		return err
	}
	if err := validateFeedbackReceipt(created, anonymousID, http.StatusCreated); err != nil {
		return fmt.Errorf("anonymous submission: %w", err)
	}
	replay, err := a.submit(anonymous, anonymousBody)
	if err != nil {
		return err
	}
	if err := validateFeedbackReceipt(replay, anonymousID, http.StatusOK); err != nil {
		return fmt.Errorf("anonymous exact replay: %w", err)
	}
	if text(created.body["receivedAt"]) != text(replay.body["receivedAt"]) {
		return errors.New("anonymous replay changed the original receipt")
	}

	operatorCookies := []*http.Cookie{a.suite.session.cookie}
	operatorContext, err := a.bootstrap(operatorCookies)
	if err != nil {
		return err
	}
	a.session = operatorContext
	a.sessionID, err = newFeedbackSubmissionID()
	if err != nil {
		return err
	}
	a.sessionBody = feedbackPayload(a.sessionID)
	sessionResult, err := a.submit(a.session, a.sessionBody)
	if err != nil {
		return err
	}
	if err := validateFeedbackReceipt(sessionResult, a.sessionID, http.StatusCreated); err != nil {
		return fmt.Errorf("session-bound public submission: %w", err)
	}
	a.suite.record("F01", map[string]string{"anonymous": "201", "anonymousReplay": "200_same_receipt", "sessionBoundPublic": "201"})
	return nil
}

func (a *feedbackAcceptance) f02() error {
	changed := feedbackPayload(a.anonymousID)
	changed["message"] = "Please contact me about the revised request."
	result, err := a.submit(a.anonymous, changed)
	if err != nil {
		return err
	}
	if err := validateFeedbackProblem(result, http.StatusConflict, "SubmissionIdentityConflict"); err != nil {
		return err
	}
	a.suite.record("F02", map[string]string{"sameIDDifferentContent": "409 SubmissionIdentityConflict"})
	return nil
}

func (a *feedbackAcceptance) f03() error {
	invalidContext, err := a.bootstrap(nil)
	if err != nil {
		return err
	}
	invalidID, err := newFeedbackSubmissionID()
	if err != nil {
		return err
	}
	invalid := feedbackPayload(invalidID)
	invalid["name"] = ""
	invalidResult, err := a.submit(invalidContext, invalid)
	if err != nil {
		return err
	}
	if err := validateFeedbackProblem(invalidResult, http.StatusUnprocessableEntity, "InvalidFeedback"); err != nil {
		return err
	}
	if len(object(invalidResult.body["fieldErrors"])) == 0 {
		return errors.New("InvalidFeedback omitted fieldErrors")
	}

	consentContext, err := a.bootstrap(nil)
	if err != nil {
		return err
	}
	consentID, err := newFeedbackSubmissionID()
	if err != nil {
		return err
	}
	withoutConsent := feedbackPayload(consentID)
	delete(withoutConsent, "consent")
	consentResult, err := a.submit(consentContext, withoutConsent)
	if err != nil {
		return err
	}
	if err := validateFeedbackProblem(consentResult, http.StatusUnprocessableEntity, "ConsentRequired"); err != nil {
		return err
	}
	a.suite.record("F03", map[string]string{"invalidField": "422 InvalidFeedback", "missingConsent": "422 ConsentRequired"})
	return nil
}

func (a *feedbackAcceptance) f04() error {
	honeypotContext, err := a.bootstrap(nil)
	if err != nil {
		return err
	}
	honeypotID, err := newFeedbackSubmissionID()
	if err != nil {
		return err
	}
	honeypot := feedbackPayload(honeypotID)
	honeypot["website"] = "https://trap.invalid"
	honeypotResult, err := a.submit(honeypotContext, honeypot)
	if err != nil {
		return err
	}
	if err := validateFeedbackReceipt(honeypotResult, honeypotID, http.StatusCreated); err != nil {
		return fmt.Errorf("honeypot acknowledgement: %w", err)
	}
	if len(honeypotResult.body) != 2 {
		return fmt.Errorf("honeypot response exposed extra fields: %v", honeypotResult.body)
	}

	originContext, err := a.bootstrap(nil)
	if err != nil {
		return err
	}
	originID, err := newFeedbackSubmissionID()
	if err != nil {
		return err
	}
	originResult, err := a.submitWithOrigin(originContext, feedbackPayload(originID), "https://feedback-origin.invalid")
	if err != nil {
		return err
	}
	if err := validateFeedbackProblem(originResult, http.StatusForbidden, "InvalidOrigin"); err != nil {
		return err
	}

	metadataContext, err := a.bootstrap(nil)
	if err != nil {
		return err
	}
	metadataID, err := newFeedbackSubmissionID()
	if err != nil {
		return err
	}
	metadataResult, err := a.submitWithFetchSite(metadataContext, feedbackPayload(metadataID), "cross-site")
	if err != nil {
		return err
	}
	if err := validateFeedbackProblem(metadataResult, http.StatusForbidden, "InvalidOrigin"); err != nil {
		return err
	}

	csrfContext, err := a.bootstrap(nil)
	if err != nil {
		return err
	}
	csrfID, err := newFeedbackSubmissionID()
	if err != nil {
		return err
	}
	csrfResult, err := a.submitWithoutCSRF(csrfContext, feedbackPayload(csrfID))
	if err != nil {
		return err
	}
	if err := validateFeedbackProblem(csrfResult, http.StatusForbidden, "InvalidCsrfToken"); err != nil {
		return err
	}

	boundaryContext, err := a.bootstrap(nil)
	if err != nil {
		return err
	}
	boundaryID, err := newFeedbackSubmissionID()
	if err != nil {
		return err
	}
	boundaryBody, err := paddedFeedbackJSON(feedbackPayload(boundaryID), feedbackBodyLimit)
	if err != nil {
		return err
	}
	boundaryResult, err := a.submitRaw(boundaryContext, boundaryBody)
	if err != nil {
		return err
	}
	if err := validateFeedbackReceipt(boundaryResult, boundaryID, http.StatusCreated); err != nil {
		return fmt.Errorf("exact 16 KiB submission: %w", err)
	}

	largeContext, err := a.bootstrap(nil)
	if err != nil {
		return err
	}
	largeID, err := newFeedbackSubmissionID()
	if err != nil {
		return err
	}
	largeBody, err := paddedFeedbackJSON(feedbackPayload(largeID), feedbackBodyLimit+1)
	if err != nil {
		return err
	}
	largeResult, err := a.submitRaw(largeContext, largeBody)
	if err != nil {
		return err
	}
	if err := validateFeedbackProblem(largeResult, http.StatusRequestEntityTooLarge, "PayloadTooLarge"); err != nil {
		return err
	}
	a.suite.record("F04", map[string]string{"honeypot": "201_plausible", "origin": "403 InvalidOrigin", "fetchMetadata": "403 InvalidOrigin", "csrf": "403 InvalidCsrfToken", "bodyLimit": "16384_accepted_16385_rejected"})
	return nil
}

func (a *feedbackAcceptance) f05() error {
	anonymous, err := a.bootstrap(nil)
	if err != nil {
		return err
	}
	anonymousID, err := newFeedbackSubmissionID()
	if err != nil {
		return err
	}
	anonymousBody := feedbackPayload(anonymousID)
	for attempt := 0; attempt < 5; attempt++ {
		result, submitErr := a.submit(anonymous, anonymousBody)
		if submitErr != nil {
			return submitErr
		}
		want := http.StatusOK
		if attempt == 0 {
			want = http.StatusCreated
		}
		if err := validateFeedbackReceipt(result, anonymousID, want); err != nil {
			return fmt.Errorf("anonymous context attempt %d: %w", attempt+1, err)
		}
	}
	limitedAnonymous, err := a.submit(anonymous, anonymousBody)
	if err != nil {
		return err
	}
	anonymousRetry, err := validateFeedbackRateLimit(limitedAnonymous)
	if err != nil {
		return fmt.Errorf("anonymous sixth attempt: %w", err)
	}

	for attempt := 1; attempt < 5; attempt++ {
		result, submitErr := a.submit(a.session, a.sessionBody)
		if submitErr != nil {
			return submitErr
		}
		if err := validateFeedbackReceipt(result, a.sessionID, http.StatusOK); err != nil {
			return fmt.Errorf("session context attempt %d: %w", attempt+1, err)
		}
	}
	limitedSession, err := a.submit(a.session, a.sessionBody)
	if err != nil {
		return err
	}
	sessionRetry, err := validateFeedbackRateLimit(limitedSession)
	if err != nil {
		return fmt.Errorf("session sixth attempt: %w", err)
	}
	a.suite.record("F05", map[string]string{
		"anonymousSixth":             "429 FeedbackRateLimited",
		"anonymousRetryAfterSeconds": strconv.Itoa(anonymousRetry),
		"sessionSixth":               "429 FeedbackRateLimited",
		"retryAfterSeconds":          strconv.Itoa(sessionRetry),
	})
	return nil
}

func (a *feedbackAcceptance) bootstrap(cookies []*http.Cookie) (feedbackContext, error) {
	result, err := a.suite.exchange(http.MethodGet, "/api/operator/csrf", "", nil, cookies)
	if err != nil {
		return feedbackContext{}, err
	}
	if result.status != http.StatusOK || text(result.body["csrfToken"]) == "" {
		return feedbackContext{}, fmt.Errorf("CSRF bootstrap returned %d %s without a token", result.status, result.bytes)
	}
	if len(cookies) == 0 {
		if cookieNamed(result.cookies, preauthCookie) == nil {
			return feedbackContext{}, errors.New("anonymous CSRF bootstrap did not issue a context cookie")
		}
		return feedbackContext{cookies: result.cookies, csrf: text(result.body["csrfToken"])}, nil
	}
	return feedbackContext{cookies: cookies, csrf: text(result.body["csrfToken"])}, nil
}

func (a *feedbackAcceptance) submit(context feedbackContext, payload map[string]any) (response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return response{}, err
	}
	return a.submitRaw(context, body)
}

func (a *feedbackAcceptance) submitRaw(context feedbackContext, body []byte) (response, error) {
	return a.exchange(context, string(body), a.suite.origin(), "same-origin", true)
}

func (a *feedbackAcceptance) submitWithOrigin(context feedbackContext, payload map[string]any, origin string) (response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return response{}, err
	}
	return a.exchange(context, string(body), origin, "same-origin", true)
}

func (a *feedbackAcceptance) submitWithFetchSite(context feedbackContext, payload map[string]any, site string) (response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return response{}, err
	}
	return a.exchange(context, string(body), a.suite.origin(), site, true)
}

func (a *feedbackAcceptance) submitWithoutCSRF(context feedbackContext, payload map[string]any) (response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return response{}, err
	}
	return a.exchange(context, string(body), a.suite.origin(), "same-origin", false)
}

func (a *feedbackAcceptance) exchange(context feedbackContext, body, origin, site string, includeCSRF bool) (response, error) {
	headers := map[string]string{"Origin": origin, "Sec-Fetch-Site": site}
	if includeCSRF {
		headers[csrfHeader] = context.csrf
	}
	return a.suite.exchange(http.MethodPost, "/api/feedback", body, headers, context.cookies)
}

func feedbackPayload(submissionID string) map[string]any {
	return map[string]any{
		"submissionID": submissionID,
		"name":         "Factory acceptance",
		"email":        "feedback-acceptance@example.org",
		"phone":        "+7 (999) 123-45-67",
		"message":      "Please contact me about this synthetic acceptance request.",
		"consent":      map[string]any{"accepted": true, "documentVersion": "v1"},
		"website":      "",
	}
}

func newFeedbackSubmissionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate feedback submission ID: %w", err)
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func paddedFeedbackJSON(payload map[string]any, size int) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if size < len(body) {
		return nil, fmt.Errorf("feedback payload is %d bytes and exceeds requested size %d", len(body), size)
	}
	return append(body, bytes.Repeat([]byte(" "), size-len(body))...), nil
}

func validateFeedbackReceipt(result response, submissionID string, status int) error {
	if result.status != status {
		return fmt.Errorf("status=%d body=%s, want %d", result.status, result.bytes, status)
	}
	if text(result.body["submissionID"]) != submissionID {
		return fmt.Errorf("receipt submissionID=%q, want %q", text(result.body["submissionID"]), submissionID)
	}
	receivedAt := text(result.body["receivedAt"])
	parsed, err := time.Parse(time.RFC3339Nano, receivedAt)
	if err != nil {
		return fmt.Errorf("receipt has invalid receivedAt %q: %w", receivedAt, err)
	}
	_, offset := parsed.Zone()
	if offset != 3*60*60 {
		return fmt.Errorf("receipt receivedAt %q does not use Moscow UTC+03:00", receivedAt)
	}
	if !strings.Contains(strings.ToLower(result.header.Get("Cache-Control")), "no-store") {
		return errors.New("feedback receipt omitted Cache-Control: no-store")
	}
	return nil
}

func validateFeedbackProblem(result response, status int, code string) error {
	if result.status != status {
		return fmt.Errorf("status=%d body=%s, want %d %s", result.status, result.bytes, status, code)
	}
	if problemCode(result) != code {
		return fmt.Errorf("problem code=%q, want %q; body=%s", problemCode(result), code, result.bytes)
	}
	if text(result.body["requestID"]) == "" {
		return fmt.Errorf("%s omitted requestID", code)
	}
	if !strings.Contains(strings.ToLower(result.header.Get("Cache-Control")), "no-store") {
		return fmt.Errorf("%s omitted Cache-Control: no-store", code)
	}
	if !strings.Contains(strings.ToLower(result.header.Get("Content-Type")), "application/json") {
		return fmt.Errorf("%s response Content-Type=%q, want application/json", code, result.header.Get("Content-Type"))
	}
	return nil
}

func validateFeedbackRateLimit(result response) (int, error) {
	if err := validateFeedbackProblem(result, http.StatusTooManyRequests, "FeedbackRateLimited"); err != nil {
		return 0, err
	}
	seconds, err := strconv.Atoi(result.header.Get("Retry-After"))
	if err != nil || seconds < 1 {
		return 0, fmt.Errorf("429 Retry-After=%q, want positive seconds", result.header.Get("Retry-After"))
	}
	return seconds, nil
}
