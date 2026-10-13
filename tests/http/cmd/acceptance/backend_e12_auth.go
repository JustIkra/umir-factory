package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/JustIkra/umir-factory/tests/http/internal/faultproxy"
)

type backendE12Assignment struct {
	RestaurantID      string   `json:"restaurantID"`
	AssignmentID      string   `json:"assignmentID"`
	AssignmentVersion string   `json:"assignmentVersion"`
	Roles             []string `json:"roles"`
}

type backendE12Membership struct {
	MembershipID      string                 `json:"membershipID"`
	MembershipVersion string                 `json:"membershipVersion"`
	UserID            string                 `json:"userID"`
	OrgID             string                 `json:"orgID"`
	OrgRoles          []string               `json:"orgRoles"`
	Assignments       []backendE12Assignment `json:"assignments"`
	State             string                 `json:"state"`
}

func backendE12Incomplete(id, request string) error {
	return fmt.Errorf("%w: %s: %s", errBackendScenarioUnimplemented, id, request)
}

func backendE12Input(input backendScenarioInput, id string) error {
	if input.Session == nil || input.Client == nil || len(input.Scopes) < 2 {
		return backendE12Incomplete(id, "real case-local scopes, authenticated session and qualified owner client required")
	}
	return nil
}

func backendE12Command() (string, error) {
	id, err := faultproxy.NewUUID()
	if err != nil {
		return "", err
	}
	return "e12-" + id, nil
}

func backendE12User(ctx context.Context, session *backendSession, expectedEmail string) (string, error) {
	data, err := session.call(ctx, "me", nil, nil, nil, 200)
	if err != nil {
		return "", err
	}
	user, ok := data["user"].(map[string]any)
	if !ok {
		return "", faultproxy.ErrProtocol
	}
	if expectedEmail != "" && user["email"] != expectedEmail {
		return "", errors.New("invited identity differs from verified email")
	}
	id, ok := user["userID"].(string)
	if !ok || !faultproxy.UUID(id) {
		return "", faultproxy.ErrProtocol
	}
	return id, nil
}

func backendE12MembershipFact(raw []byte, scope backendScope, user string, orgRoles, roles []string) (backendE12Membership, error) {
	wire, err := backendWire.ReadFile("backend-public.json")
	if err != nil || faultproxy.ValidateSchema(wire, "#/components/schemas/AccessView", raw, 32) != nil {
		return backendE12Membership{}, errors.New("complete typed access facts required")
	}
	var view struct {
		Memberships []backendE12Membership `json:"memberships"`
		Restaurants []struct {
			OrgID        string `json:"orgID"`
			RestaurantID string `json:"restaurantID"`
		} `json:"restaurants"`
	}
	if json.Unmarshal(raw, &view) != nil {
		return backendE12Membership{}, faultproxy.ErrProtocol
	}
	var member backendE12Membership
	matches := 0
	for _, candidate := range view.Memberships {
		if candidate.OrgID == scope.OrgID {
			matches++
			member = candidate
		}
	}
	if matches != 1 || member.UserID != user || member.State != "active" || !backendE12RolesEqual(member.OrgRoles, orgRoles) {
		return backendE12Membership{}, errors.New("membership identity, state or org grant differs")
	}
	if len(roles) == 0 {
		if len(orgRoles) == 0 && len(member.Assignments) != 0 {
			return backendE12Membership{}, errors.New("unexpected restaurant grants")
		}
		if len(orgRoles) > 0 {
			seen := map[string]bool{}
			for _, assignment := range member.Assignments {
				owned := false
				for _, restaurant := range view.Restaurants {
					if restaurant.RestaurantID == assignment.RestaurantID && restaurant.OrgID == scope.OrgID {
						owned = true
					}
				}
				if !owned || seen[assignment.RestaurantID] {
					return backendE12Membership{}, errors.New("creator restaurant grant outside its own organization or duplicated")
				}
				seen[assignment.RestaurantID] = true
			}
		}
	} else if len(member.Assignments) != 1 || member.Assignments[0].RestaurantID != scope.RestaurantID || !backendE12RolesEqual(member.Assignments[0].Roles, roles) {
		return backendE12Membership{}, errors.New("saved restaurant roles differ from invitation")
	}
	return member, nil
}

func backendE12RolesEqual(actual, expected []string) bool {
	a, b := slices.Clone(actual), slices.Clone(expected)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func backendE12AccessFact(ctx context.Context, session *backendSession, scope backendScope, user string, orgRoles, roles []string) (backendE12Membership, error) {
	if _, err := session.call(ctx, "access", nil, nil, nil, 200); err != nil {
		return backendE12Membership{}, err
	}
	return backendE12MembershipFact(session.publicReply().Body, scope, user, orgRoles, roles)
}

func backendE12Refuse(ctx context.Context, session *backendSession, operation string, params map[string]string, payload any, statuses ...int) error {
	op, ok := session.operations[operation]
	if !ok {
		return errors.New("frozen operation unavailable")
	}
	route := op.Path
	for key, value := range params {
		route = strings.ReplaceAll(route, "{"+key+"}", url.PathEscape(value))
	}
	if strings.ContainsAny(route, "{}") {
		return faultproxy.ErrProtocol
	}
	var raw []byte
	if payload != nil {
		var err error
		raw, err = json.Marshal(payload)
		if err != nil {
			return faultproxy.ErrProtocol
		}
	}
	req, err := http.NewRequestWithContext(ctx, op.Method, session.base+route, bytes.NewReader(raw))
	if err != nil {
		return faultproxy.ErrProtocol
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if op.Method != http.MethodGet {
		req.Header.Set("Origin", session.origin)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("X-CSRF-Token", session.csrfValue())
	}
	res, err := session.client.Do(req)
	if err != nil {
		return errors.New("refusal transport outcome unknown")
	}
	body, readErr := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	closeErr := res.Body.Close()
	if errors.Join(readErr, closeErr) != nil || len(body) > 1<<20 {
		return errors.New("refusal response unavailable")
	}
	if !slices.Contains(statuses, res.StatusCode) {
		return errors.New("expected owner refusal absent")
	}
	if faultproxy.ValidateSchema(session.wire, "#/components/schemas/Error", body, 32) != nil {
		return errors.New("typed refusal absent")
	}
	var refusal struct {
		Outcome string `json:"outcome"`
		Code    string `json:"code"`
	}
	if json.Unmarshal(body, &refusal) != nil {
		return faultproxy.ErrProtocol
	}
	expected := "not_committed"
	if op.Method == http.MethodGet {
		expected = "unavailable"
	}
	if refusal.Outcome != expected || !slices.Contains([]string{"Forbidden", "InvalidArgument", "InvalidRequest", "InvalidProof", "Unauthenticated"}, refusal.Code) {
		return errors.New("refusal did not establish the required auth/input boundary")
	}
	return nil
}

func backendE12Admin(ctx context.Context, input backendScenarioInput) error {
	const id = "BE-E1-05"
	if err := backendE12Input(input, id); err != nil {
		return err
	}
	if _, err := backendWaitMilliseconds(ctx, 0); err != nil {
		return err
	}
	command, err := backendE12Command()
	if err != nil {
		return err
	}
	employee, err := input.Session.enrollUser(ctx, input.Client, input.Scopes[0], command+"@umir.invalid", []string{"Warehouse"})
	if err != nil {
		return err
	}
	user, err := backendE12User(ctx, employee, command+"@umir.invalid")
	if err != nil {
		return err
	}
	bMember, err := backendE12AccessFact(ctx, employee, input.Scopes[0], user, nil, []string{"Warehouse"})
	if err != nil {
		return err
	}
	own, err := employee.organization(ctx, 0, "Europe/Moscow")
	if err != nil {
		return err
	}
	aMember, err := backendE12AccessFact(ctx, employee, own, user, []string{"Admin"}, nil)
	if err != nil {
		return err
	}
	if _, err := employee.call(ctx, "organization", map[string]string{"orgID": own.OrgID}, nil, nil, 200); err != nil {
		return err
	}
	command, err = backendE12Command()
	if err != nil {
		return err
	}
	created, err := employee.call(ctx, "createRestaurant", map[string]string{"orgID": own.OrgID}, map[string]any{"commandID": command, "name": "E12 Admin Restaurant"}, nil, 201)
	if err != nil {
		return err
	}
	operation, ok := created["operationID"].(string)
	if !ok || !faultproxy.UUID(operation) || created["orgID"] != own.OrgID {
		return faultproxy.ErrProtocol
	}
	createdIDs, ok := created["restaurantIDs"].([]any)
	if !ok || len(createdIDs) != 1 {
		return faultproxy.ErrProtocol
	}
	createdID, ok := createdIDs[0].(string)
	if !ok || !faultproxy.UUID(createdID) || createdID == own.RestaurantID {
		return faultproxy.ErrProtocol
	}
	if err := employee.waitLifecycle(ctx, own.OrgID, operation); err != nil {
		return err
	}
	command, err = backendE12Command()
	if err != nil {
		return err
	}
	aMember, err = backendE12AccessFact(ctx, employee, own, user, []string{"Admin"}, nil)
	if err != nil {
		return err
	}
	ownedAssignments := []any{}
	for _, assignment := range aMember.Assignments {
		ownedAssignments = append(ownedAssignments, map[string]any{"restaurantID": assignment.RestaurantID, "roles": assignment.Roles})
	}
	if _, err := employee.call(ctx, "updateMembership", map[string]string{"orgID": own.OrgID, "membershipID": aMember.MembershipID}, map[string]any{"commandID": command, "expectedMembershipVersion": aMember.MembershipVersion, "orgRoles": []string{"Admin"}, "assignments": ownedAssignments}, nil, 200); err != nil {
		return err
	}
	command, err = backendE12Command()
	if err != nil {
		return err
	}
	if err := backendE12Refuse(ctx, employee, "createRestaurant", map[string]string{"orgID": input.Scopes[0].OrgID}, map[string]any{"commandID": command, "name": "Denied Restaurant"}, 403); err != nil {
		return err
	}
	command, err = backendE12Command()
	if err != nil {
		return err
	}
	if err := backendE12Refuse(ctx, employee, "updateMembership", map[string]string{"orgID": input.Scopes[0].OrgID, "membershipID": bMember.MembershipID}, map[string]any{"commandID": command, "expectedMembershipVersion": bMember.MembershipVersion, "orgRoles": []string{"Admin"}, "assignments": []any{}}, 403); err != nil {
		return err
	}
	unchanged, err := backendE12AccessFact(ctx, employee, input.Scopes[0], user, nil, []string{"Warehouse"})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(bMember, unchanged) {
		return errors.New("cross-org management refusal changed membership")
	}
	if err := employee.scopeSession(ctx, []backendScope{own, {OrgID: own.OrgID, RestaurantID: createdID}}); err != nil {
		return err
	}
	if err := employee.scopeSession(ctx, input.Scopes[:1]); err != nil {
		return err
	}
	for _, op := range []string{"inventory", "menu"} {
		if err := backendE12Refuse(ctx, employee, op, map[string]string{"restaurantID": input.Scopes[1].RestaurantID}, nil, 403); err != nil {
			return err
		}
	}
	return nil
}

type backendE12InvitationProof struct {
	Session      *backendSession
	InvitationID string
	ChallengeID  string
	Proof        string
}

func backendE12Invitation(ctx context.Context, input backendScenarioInput, scope backendScope, email string) (map[string]any, error) {
	command, err := backendE12Command()
	if err != nil {
		return nil, err
	}
	return input.Session.call(ctx, "createInvitation", map[string]string{"orgID": scope.OrgID}, map[string]any{"commandID": command, "email": email, "assignments": []any{map[string]any{"restaurantID": scope.RestaurantID, "roles": []string{"Warehouse", "Waiter"}}}}, nil, 201)
}

func backendE12Proof(ctx context.Context, input backendScenarioInput, invitation map[string]any) (backendE12InvitationProof, error) {
	var result backendE12InvitationProof
	id, ok := invitation["invitationID"].(string)
	if !ok || !faultproxy.UUID(id) {
		return result, faultproxy.ErrProtocol
	}
	invited := newBackendSession(input.Session.base, input.Session.origin)
	if invited == nil {
		return result, errors.New("invited browser unavailable")
	}
	invited.probe = input.Session.probe
	invited.client.Transport = input.Session.client.Transport
	if err := invited.csrfToken(ctx); err != nil {
		return result, err
	}
	command, err := backendE12Command()
	if err != nil {
		return result, err
	}
	challenge, err := invited.call(ctx, "beginInvitationChallenge", map[string]string{"invitationID": id}, map[string]any{"commandID": command, "invitationID": id}, nil, 200)
	if err != nil {
		return result, err
	}
	challengeID, ok := challenge["challengeID"].(string)
	if !ok || !faultproxy.UUID(challengeID) {
		return result, faultproxy.ErrProtocol
	}
	expiry, ok := challenge["expiresAt"].(string)
	if !ok {
		return result, faultproxy.ErrProtocol
	}
	projection, err := input.Client.discoverJob(ctx, map[string]any{"kind": "command", "challengeID": challengeID, "commandID": command, "operationID": "beginInvitationChallenge"}, expiry)
	if err != nil {
		return result, err
	}
	jobs, ok := projection["jobs"].([]any)
	if !ok || len(jobs) != 1 {
		return result, errors.New("invitation job correlation unavailable")
	}
	job, ok := jobs[0].(map[string]any)
	if !ok {
		return result, faultproxy.ErrProtocol
	}
	clock, err := input.Client.clock(ctx, "auth")
	if err != nil {
		return result, err
	}
	otp, err := input.Client.otp(ctx, job, clock)
	if err != nil {
		return result, err
	}
	command, err = backendE12Command()
	if err != nil {
		return result, err
	}
	verified, err := invited.call(ctx, "verifyChallenge", map[string]string{"challengeID": challengeID}, map[string]any{"commandID": command, "challengeID": challengeID, "code": otp}, nil, 200)
	if err != nil {
		return result, err
	}
	proof, ok := verified["proof"].(string)
	if !ok || proof == "" {
		return result, faultproxy.ErrProtocol
	}
	return backendE12InvitationProof{invited, id, challengeID, proof}, nil
}

func backendE12InvitedRoles(ctx context.Context, input backendScenarioInput) error {
	const id = "BE-E1-07"
	if err := backendE12Input(input, id); err != nil {
		return err
	}
	if _, err := backendWaitMilliseconds(ctx, 0); err != nil {
		return err
	}
	command, err := backendE12Command()
	if err != nil {
		return err
	}
	email := command + "@example.invalid"
	assignments := []any{map[string]any{"restaurantID": input.Scopes[0].RestaurantID, "roles": []string{"Warehouse", "Waiter"}}}
	for _, kind := range []string{"admin", "duplicate", "foreign"} {
		command, err = backendE12Command()
		if err != nil {
			return err
		}
		body := map[string]any{"commandID": command, "email": email, "assignments": assignments}
		statuses := []int{400, 422}
		switch kind {
		case "admin":
			body["orgRoles"] = []string{"Admin"}
		case "duplicate":
			body["assignments"] = []any{assignments[0], assignments[0]}
		case "foreign":
			body["assignments"] = []any{map[string]any{"restaurantID": input.Scopes[1].RestaurantID, "roles": []string{"Warehouse", "Waiter"}}}
			statuses = []int{400, 403, 422}
		}
		if err := backendE12Refuse(ctx, input.Session, "createInvitation", map[string]string{"orgID": input.Scopes[0].OrgID}, body, statuses...); err != nil {
			return err
		}
	}
	invitation, err := backendE12Invitation(ctx, input, input.Scopes[0], email)
	if err != nil {
		return err
	}
	proof, err := backendE12Proof(ctx, input, invitation)
	if err != nil {
		return err
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		return errors.New("private entropy unavailable")
	}
	password := hex.EncodeToString(secret)
	clear(secret)
	completion := func(command string) map[string]any {
		return map[string]any{"commandID": command, "challengeID": proof.ChallengeID, "proof": proof.Proof, "password": password, "displayName": "E12 Invited Worker"}
	}
	other := newBackendSession(input.Session.base, input.Session.origin)
	if other == nil {
		return errors.New("independent browser unavailable")
	}
	other.probe = input.Session.probe
	other.client.Transport = input.Session.client.Transport
	if err := other.csrfToken(ctx); err != nil {
		return err
	}
	command, err = backendE12Command()
	if err != nil {
		return err
	}
	if err := backendE12Refuse(ctx, other, "completeInvitedUser", map[string]string{"challengeID": proof.ChallengeID}, completion(command), 400, 401, 403, 422); err != nil {
		return err
	}
	command, err = backendE12Command()
	if err != nil {
		return err
	}
	if err := backendE12Refuse(ctx, input.Session, "acceptInvitation", map[string]string{"invitationID": proof.InvitationID}, map[string]any{"commandID": command, "challengeID": proof.ChallengeID, "proof": proof.Proof}, 400, 401, 403, 422); err != nil {
		return err
	}
	command, err = backendE12Command()
	if err != nil {
		return err
	}
	elevated := completion(command)
	elevated["orgRoles"] = []string{"Admin"}
	if err := backendE12Refuse(ctx, proof.Session, "completeInvitedUser", map[string]string{"challengeID": proof.ChallengeID}, elevated, 400, 422); err != nil {
		return err
	}
	command, err = backendE12Command()
	if err != nil {
		return err
	}
	if _, err := proof.Session.call(ctx, "completeInvitedUser", map[string]string{"challengeID": proof.ChallengeID}, completion(command), nil, 200); err != nil {
		return err
	}
	if err := proof.Session.csrfToken(ctx); err != nil {
		return err
	}
	user, err := backendE12User(ctx, proof.Session, email)
	if err != nil {
		return err
	}
	if _, err := backendE12AccessFact(ctx, proof.Session, input.Scopes[0], user, nil, []string{"Warehouse", "Waiter"}); err != nil {
		return err
	}
	var all struct {
		Memberships []backendE12Membership `json:"memberships"`
	}
	if json.Unmarshal(proof.Session.publicReply().Body, &all) != nil || len(all.Memberships) != 1 {
		return errors.New("invitation granted additional scope or membership")
	}
	for _, member := range all.Memberships {
		if len(member.OrgRoles) != 0 {
			return errors.New("invitation granted Admin")
		}
	}
	return nil
}
