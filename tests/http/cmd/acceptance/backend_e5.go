package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/JustIkra/umir-factory/tests/http/internal/faultproxy"
)

func runBackendE5(ctx context.Context, input backendScenarioInput, id string) error {
	group, err := backendScenarioGroup(id)
	if err != nil || group != "e5" {
		return faultproxy.ErrProtocol
	}
	if request := e5OwnerRequest(id); request != "" {
		return fmt.Errorf("%w: %s: %s", errBackendScenarioUnimplemented, id, request)
	}
	if input.Session == nil || input.Client == nil || len(input.Scopes) < 2 {
		return fmt.Errorf("%w: %s: E5 scenario setup unavailable", errBackendScenarioUnimplemented, id)
	}
	if _, err := backendWaitMilliseconds(ctx, 0); err != nil {
		return err
	}
	for _, scope := range input.Scopes {
		if !faultproxy.UUID(scope.OrgID) || !faultproxy.UUID(scope.RestaurantID) {
			return errors.New("E5 scenario setup has invalid scope")
		}
	}
	switch id {
	case "BE-E5-08":
		return e5BusinessRestart(ctx, input)
	case "BE-E5-13":
		return e5FeedbackRestart(ctx, input)
	}
	return faultproxy.ErrProtocol
}

func e5OwnerRequest(id string) string {
	switch id {
	case "BE-E5-01":
		return "Auth owner: reviewed aggregate/entity and invitation-job observations, and canonical command-lookup namespace helper for lost-result/revision/entity/job comparison"
	case "BE-E5-02":
		return "Auth owner: reviewed challenge/proof counter, User/Membership creation, atomic outbox and reset-session observations across restart and expired completion"
	case "BE-E5-03":
		return "Auth/SMTP owner: reviewed pre-send job discovery/barrier and complete attempts/next-retry observations for DATA acknowledgment loss, interrupted sending, 60/300-second delays, max3 and synthetic-recipient totals"
	case "BE-E5-04":
		return "Auth owner/profile: independently READY isolated definite/ambiguous storage failure, corrupt/unknown schema, invalid-reference, legacy migration and second-writer fixtures; reviewed admission and identity/deadline observations"
	case "BE-E5-05":
		return "Auth owner/runner: isolated session-capacity descriptor and persisted session deadline/live-device observations; independent user credentials and 33-device/deadline/global-revocation orchestration within reviewed case budget"
	case "BE-E5-06":
		return "Auth owner: oversized-input verifier/counter observation and deterministic two-active/eight-queued verification barrier, with isolated login/password rolling-rate fixture"
	case "BE-E5-07":
		return "Auth owner/runner: isolated bounded target/global invitation/OTP-capacity fixture and reviewed counters, one-use proof/browser binding, delivery/display comparison and fixed expiry observations for all frozen limits"
	case "BE-E5-09":
		return "Auth/Gateway owner: reviewed unavailable-cleanup-peer barrier and bounded SSE access-ended/EOF observation, with exact affected-ID and unrelated-scope facts"
	case "BE-E5-10":
		return "Auth owner/helper: approved canonical command-lookup namespace encoder and original accepted LifecycleResult/revision observation after lost reply, with actor-isolated unauthorized polling context"
	case "BE-E5-11":
		return "Auth/business owner: reviewed partial-cleanup/old-registration barrier and current-epoch close/intent observations; deterministic peer restart race and bounded retry completion"
	case "BE-E5-12":
		return "Auth/Serving/Warehouse owner: reviewed pre-send scoped-job barrier/suppression facts and late unknown-commit barrier with complete attempt-count/outcome observations across cutoff and cleanup failure"
	case "BE-E5-14":
		return "Feedback owner/profile: independently READY isolated legacy journal migration with retained namespace/fingerprint/job facts, redacted synthetic Telegram/SMTP privacy observations and cascade retention proof"
	case "BE-E5-15":
		return "Auth owner/helper: approved canonical command-lookup namespace encoder and original completion/outcome observation for actor-isolated cookie restoration, conflict and denied replay comparison"
	}
	return ""
}
