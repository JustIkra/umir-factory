package main

import (
	"context"
	"errors"
	"fmt"
)

func runBackendE34(ctx context.Context, input backendScenarioInput, id string) error {
	if request, ok := e34OwnerRequests[id]; ok {
		return fmt.Errorf("%w: %s: %s", errBackendScenarioUnimplemented, id, request)
	}
	body, ok := e34Bodies[id]
	if !ok {
		return fmt.Errorf("unassigned e34 case: %s", id)
	}
	if ctx == nil || input.Session == nil || input.Client == nil || len(input.Scopes) != 2 || input.Scopes[0] == input.Scopes[1] {
		return fmt.Errorf("%w: %s: real session, client and two isolated scopes required", errBackendScenarioUnimplemented, id)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	facts := &e34Facts{}
	scenario := e34Scenario{ctx: context.WithValue(ctx, e34FactsKey{}, facts), input: input}
	caseErr := body(&scenario)
	evidenceErr := facts.save(input.Client, id, caseErr == nil)
	if err := errors.Join(caseErr, evidenceErr); err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}
	return nil
}

var e34Bodies = map[string]func(*e34Scenario) error{
	"BE-E3-01": e34FirstFree,
	"BE-E3-02": e34CatalogIsolation,
	"BE-E3-03": e34Recommendations,
	"BE-E3-05": e34Deadline,
	"BE-E3-08": e34HumanWriteoff,
	"BE-E4-01": e34Stock,
	"BE-E4-04": e34Coverage,
	"BE-E4-09": e34Settings,
	"BE-E4-10": e34Calendar,
	"BE-E4-11": e34Civil,
	"BE-E4-12": e34ShelfLife,
}

var e34OwnerRequests = map[string]string{
	"BE-E3-04": "Serving/Warehouse and integrator: reviewed passive original-proposal observation and retained unknown-attempt fixture; normal private consumer calls must carry the frozen runtime-epoch header",
	"BE-E3-06": "Gateway/Warehouse: reviewed scheduler hold and tenant-bound committed invalidation observation before any scheduler tick",
	"BE-E3-07": "Gateway/Warehouse: owner-reported active scheduler inventory and expiry-only service/Waiter refusal observations; scheduler.tick does not report active owners",
	"BE-E3-09": "Warehouse: supported retained pending writeoff admission fixture and passive command-record observation; before-commit refusal does not create a pending command",
	"BE-E3-10": "Warehouse/Serving and integrator: reviewed read-only lookup reply-loss fixture and owner dispatch observations across reset; existing fault points do not support lost GET replies",
	"BE-E3-11": "Warehouse: reviewed isolated legacy BatchExpired schemaVersion 1 admission; ordinary v2 HTTP cannot insert historical facts",
	"BE-E3-12": "Warehouse/Gateway: reviewed scheduler hold, admission barriers and tenant/epoch-bound journal, projection, index, command and committed invalidation snapshots before any fresh read",
	"BE-E4-02": "Warehouse: reviewed isolated legacy/final and excluded pending/proposed/rejected/unknown/duplicate journal admission fixture with exact RecordedAt boundaries",
	"BE-E4-03": "Warehouse and integrator: reviewed bounded >500-row catalog fixture, second actor and complete captured-report observations; private positive requests need frozen epoch/header support",
	"BE-E4-05": "Warehouse: reviewed isolated unsupported-schema, quantity-overflow and unit-mismatch journal admission and full fact accounting",
	"BE-E4-06": "Gateway/Warehouse: reviewed scheduler hold and tenant-bound committed invalidation observations spanning the post-preparation analytics fault",
	"BE-E4-07": "Gateway/Warehouse: reviewed per-restaurant summary failure and bounded downstream dispatch observations proving zero cross-org calls",
	"BE-E4-08": "Gateway/Warehouse: reviewed bounded 501-row report fixture and page-two-only failure/loop/drift/timeout mechanism",
	"BE-E4-13": "Auth/Warehouse/Serving: reviewed saved session, OTP, invitation and JWT deadline observations plus event/order/FEFO snapshots across settings updates",
	"BE-E4-14": "Warehouse/Serving: reviewed retained unknown candidate observation for delivery/order/writeoff exact retries; existing commit/unknown faults alone do not expose candidate identity/settings",
	"BE-E4-15": "Auth/Warehouse/Serving and integrator: reviewed in-flight settings capture hold/release barrier and old-epoch ordering observations; owner.fault.arm has no hold/release action",
	"BE-E4-16": "Auth/Warehouse/Serving/Gateway and integrator: independently READY missing-tzdata profile, real reviewed owner profile path, manifest/source identity, isolated runner-owned descriptor, failed-readiness probe and healthy packaged-version comparison; profile evidence was not supplied",
	"BE-E4-17": "Gateway/Warehouse: reviewed bounded dispatch observations proving invalid date/risk/limit ranges query no owner; HTTP refusal status alone cannot prove zero dispatch",
	"BE-E4-18": "Warehouse and integrator: distinct runner-owned capacity descriptor, bounded multirow fixture and real report-store snapshots for all 128 captures before/after refusals and five-minute expiry",
}
