package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"slices"

	"errors"
	"fmt"

	"github.com/JustIkra/umir-factory/tests/http/internal/faultproxy"
)

func runBackendE12(ctx context.Context, input backendScenarioInput, id string) error {
	switch id {
	case "BE-E1-05":
		return backendE12Admin(ctx, input)
	case "BE-E1-07":
		return backendE12InvitedRoles(ctx, input)
	case "BE-E1-02":
		if input.Session == nil || len(input.Scopes) < 2 {
			return errors.New("scenario setup unavailable")
		}
		return input.Session.scopeSession(ctx, input.Scopes)
	case "BE-E1-12":
		if input.Session == nil || len(input.Scopes) < 2 {
			return errors.New("scenario setup unavailable")
		}
		return input.Session.feedbackNamespaces(ctx, input.Scopes)
	}
	if request, ok := backendE12Requests[id]; ok {
		return backendE12Incomplete(id, request)
	}
	return fmt.Errorf("%w: %s", errBackendScenarioUnimplemented, id)
}

func (b *backendSession) scopeSession(ctx context.Context, scopes []backendScope) error {
	u, e := url.Parse(b.base)
	if e != nil {
		return faultproxy.ErrProtocol
	}
	before := b.client.Jar.Cookies(u)
	session := ""
	for _, cookie := range before {
		if cookie.Name == "umir_operator_session" {
			session = cookie.Value
		}
	}
	if session == "" {
		return errors.New("session unavailable")
	}
	for _, scope := range scopes {
		for _, op := range []string{"inventory", "menu"} {
			data, e := b.call(ctx, op, map[string]string{"restaurantID": scope.RestaurantID}, nil, nil, 200)
			if e != nil {
				return e
			}
			c, ok := data["context"].(map[string]any)
			if !ok || c["tenantID"] != scope.RestaurantID || c["orgID"] != scope.OrgID {
				return errors.New("scope differs from route")
			}
		}
	}
	found := false
	for _, cookie := range b.client.Jar.Cookies(u) {
		if cookie.Name == "umir_operator_session" && cookie.Value == session {
			found = true
		}
	}
	if !found {
		return errors.New("session changed across scopes")
	}
	return nil
}
func (b *backendSession) feedbackNamespaces(ctx context.Context, scopes []backendScope) error {
	id, e := faultproxy.NewUUID()
	if e != nil {
		return e
	}
	payload := map[string]any{"submissionID": id, "name": "Synthetic Operator", "email": "fixture-feedback@umir.invalid", "message": "Synthetic feedback acceptance", "consent": map[string]any{"accepted": true, "documentVersion": "e2e-fixture-v1"}}

	for _, scope := range scopes[:2] {
		saved, e := b.call(ctx, "appFeedback", map[string]string{"restaurantID": scope.RestaurantID}, payload, nil, 201)
		if e != nil {
			return e
		}
		actual, ok := saved["scope"].(map[string]any)
		if !ok || actual["tenantID"] != scope.RestaurantID || actual["orgID"] != scope.OrgID || saved["state"] != "accepted" {
			return errors.New("feedback scope differs")
		}
		receipt, ok := saved["receipt"].(map[string]any)
		if !ok || receipt["submissionID"] != id {
			return errors.New("feedback receipt differs")
		}

	}
	anonymous, e := b.call(ctx, "anonymousFeedback", nil, payload, nil, 201)
	if e != nil {
		return e
	}
	scope, ok := anonymous["scope"].(map[string]any)
	if !ok || !reflect.DeepEqual(scope, map[string]any{"kind": "anonymous"}) || anonymous["state"] != "accepted" {
		return errors.New("anonymous scope differs")
	}
	spoof := map[string]any{}
	for k, v := range payload {
		spoof[k] = v
	}
	spoof["tenantID"] = scopes[0].RestaurantID
	op := b.operations["anonymousFeedback"]
	raw, e := json.Marshal(spoof)
	if e != nil {
		return faultproxy.ErrProtocol
	}
	req, e := http.NewRequestWithContext(ctx, op.Method, b.base+op.Path, bytes.NewReader(raw))
	if e != nil {
		return faultproxy.ErrProtocol
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", b.origin)
	req.Header.Set("X-CSRF-Token", b.csrf)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	res, e := b.client.Do(req)
	if e != nil {
		return errors.New("scope spoof transport unavailable")
	}
	body, e := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	e = errors.Join(e, res.Body.Close())
	if e != nil || len(body) > 1<<20 || !slices.Contains([]int{400, 422}, res.StatusCode) || b.validateRefusal(body) != nil {
		return errors.New("scope spoof was not refused")
	}
	return nil
}
