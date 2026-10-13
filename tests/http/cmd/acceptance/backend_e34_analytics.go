package main

import (
	"errors"
	"net/url"
	"reflect"
	"sync"
	"time"
)

func e34Stock(s *e34Scenario) error {
	now, err := s.clock()
	if err != nil {
		return err
	}
	for _, product := range []struct{ id, unit string }{{"stock", "g"}, {"liquid", "ml"}} {
		if err := s.product(0, product.id, product.id, product.unit); err != nil {
			return err
		}
	}
	for _, batch := range []struct {
		product, quantity string
		expiry            time.Time
	}{{"stock", "3000", now}, {"stock", "2000", now.Add(time.Hour)}, {"stock", "5000", now.Add(72 * time.Hour)}, {"liquid", "4000", now.Add(time.Hour)}} {
		if _, err := s.supply(0, batch.product, batch.quantity, batch.expiry); err != nil {
			return err
		}
	}
	before, err := s.report(0, "analyticsstock", url.Values{"riskHours": {"48"}})
	if err != nil {
		return err
	}
	row, err := e34Row(before, "productID", "stock", "items")
	if err != nil {
		return err
	}
	cutoff, err := e34Duration(map[string]any{"from": e34Field(before, "context", "evaluatedAt"), "to": before["riskCutoff"]}, "from", "to")
	if err != nil {
		return err
	}
	if err := errors.Join(e34Expect("/quantity/onHand", row["onHandQuantity"], "10000"), e34Expect("/quantity/available", row["availableQuantity"], "7000"), e34Expect("/quantity/pending", row["pendingWriteoffQuantity"], "3000"), e34Expect("/quantity/expiring", row["expiringQuantity"], "2000"), e34Expect("/risk/physicalHours", cutoff, 48*time.Hour), e34Expect("/risk/physicalHours", e34Number(before["riskHours"], 48), true)); err != nil {
		return err
	}
	liquid, err := e34Row(before, "productID", "liquid", "items")
	if err != nil || liquid["baseUnit"] != "ml" || row["baseUnit"] != "g" {
		return errors.New("independent stock units missing")
	}
	for _, period := range []url.Values{{"fromDate": {"2026-03-29"}, "throughDate": {"2026-03-29"}}, {"fromDate": {"2026-10-01"}, "throughDate": {"2026-10-07"}}} {
		if _, err := s.report(0, "analyticsmovements", period); err != nil {
			return err
		}
		after, err := s.report(0, "analyticsstock", url.Values{"riskHours": {"48"}})
		if err != nil {
			return err
		}
		if err := errors.Join(e34Expect("/stock/independentOfMovementPeriod", after["items"], before["items"]), e34Expect("/stock/independentOfMovementPeriod", after["riskCutoff"], before["riskCutoff"]), e34Expect("/stock/independentOfMovementPeriod", e34Field(after, "context", "stateVersion"), e34Field(before, "context", "stateVersion"))); err != nil {
			return err
		}
	}
	return nil
}

func e34Coverage(s *e34Scenario) error {
	before, err := s.read(0, "inventory", nil)
	if err != nil {
		return err
	}
	if _, err := s.input.Client.restart(s.ctx, "warehouse"); err != nil {
		return err
	}
	var current map[string]any
	for {
		current, err = s.call(0, "inventory", nil, nil, nil, 200, 503)
		if err != nil {
			return err
		}
		if current["code"] == nil {
			break
		}
		if current["code"] != "RuntimeNotReady" {
			return errors.New("reset reconciliation failed")
		}
		if err := backendPause(s.ctx); err != nil {
			return err
		}
	}
	oldEpoch := e34Field(before, "context", "runtimeEpoch")
	newEpoch := e34Field(current, "context", "runtimeEpoch")
	if oldEpoch == nil || newEpoch == nil || oldEpoch == newEpoch {
		return errors.New("new owner epoch not observed")
	}
	report, err := s.report(0, "analyticsmovements", url.Values{"fromDate": {"2026-01-01"}, "throughDate": {"2026-01-07"}})
	if err != nil {
		return err
	}
	coverage, ok := report["coverage"].(map[string]any)
	if !ok || coverage["complete"] != false {
		return errors.New("incomplete owner coverage required")
	}
	from, ok := report["from"].(string)
	covered, other := coverage["coverageFrom"].(string)
	if !ok || !other {
		return errors.New("coverage instants missing")
	}
	start, err := time.Parse(time.RFC3339Nano, from)
	available, otherErr := time.Parse(time.RFC3339Nano, covered)
	if err != nil || otherErr != nil {
		return errors.New("coverage instants invalid")
	}
	maximum := from
	if available.After(start) {
		maximum = covered
	}
	return errors.Join(e34Expect("/coverage/complete", coverage["complete"], false), e34Expect("/coverage/availableFromMatchesMax", coverage["availableFrom"], maximum), e34Expect("/coverage/newEpochVisible", coverage["epoch"], newEpoch), e34Expect("/coverage/newEpochVisible", e34Field(report, "context", "runtimeEpoch"), newEpoch), e34Expect("/coverage/claimedFullHistory", coverage["complete"], false), e34Expect("/coverage/availableFromMatchesMax", coverage["requestedFrom"], from))
}

func e34Settings(s *e34Scenario) error {
	id, err := e34ID()
	if err != nil {
		return err
	}
	foreign, err := s.input.Session.enrollUser(s.ctx, s.input.Client, s.input.Scopes[0], "e34-admin-"+id+"@umir.invalid", []string{"Warehouse"})
	if err != nil {
		return err
	}
	foreignScope, err := foreign.organization(s.ctx, 0, "Europe/Berlin")
	if err != nil {
		return err
	}
	foreignParams := map[string]string{"orgID": foreignScope.OrgID}
	foreignBefore, err := e34HTTP(s.ctx, foreign, "organization", foreignParams, nil, nil, nil, 200)
	if err != nil {
		return err
	}
	original, err := s.org(0)
	if err != nil {
		return err
	}
	commands := make([]string, 2)
	for i := range commands {
		commands[i], err = e34ID()
		if err != nil {
			return err
		}
	}
	results := make([]map[string]any, 2)
	failures := make([]error, 2)
	var joined sync.WaitGroup
	for i := range commands {
		joined.Go(func() {
			results[i], failures[i] = e34HTTP(s.ctx, s.input.Session, "updateSettings", map[string]string{"orgID": s.input.Scopes[0].OrgID}, nil, map[string]any{"commandID": commands[i], "expectedSettingsVersion": e34Field(original, "settings", "settingsVersion"), "timeZone": []string{"America/New_York", "Europe/Berlin"}[i]}, nil, 200, 409)
		})
	}
	joined.Wait()
	if err := errors.Join(failures...); err != nil {
		return err
	}
	winners, stale := 0, 0
	for _, result := range results {
		if result["orgID"] == s.input.Scopes[0].OrgID {
			winners++
		} else if result["code"] == "SettingsChanged" && result["outcome"] == "not_committed" {
			stale++
		} else {
			return errors.New("settings race returned no exact owner outcome")
		}
	}
	if winners != 1 || stale != 1 {
		return errors.New("settings race must accept exactly one update")
	}
	saved, err := s.org(0)
	if err != nil {
		return err
	}
	for _, test := range []struct {
		session    *backendSession
		params     map[string]string
		version    any
		zone, code string
		status     int
	}{
		{s.input.Session, map[string]string{"orgID": s.input.Scopes[0].OrgID}, e34Field(original, "settings", "settingsVersion"), "Europe/Moscow", "SettingsChanged", 409},
		{s.input.Session, map[string]string{"orgID": s.input.Scopes[0].OrgID}, e34Field(saved, "settings", "settingsVersion"), "Unknown/e34-invalid", "UnknownTimeZone", 422},
		{s.input.Session, foreignParams, e34Field(foreignBefore, "settings", "settingsVersion"), "Europe/Moscow", "Forbidden", 403},
	} {
		command, err := e34ID()
		if err != nil {
			return err
		}
		refused, err := e34HTTP(s.ctx, test.session, "updateSettings", test.params, nil, map[string]any{"commandID": command, "expectedSettingsVersion": test.version, "timeZone": test.zone}, nil, test.status)
		if err != nil {
			return err
		}
		if err := e34Expect("/settings/staleFreshError", refused["code"], test.code); err != nil {
			return err
		}
	}
	unchanged, err := s.org(0)
	if err != nil {
		return err
	}
	if err := e34Expect("/settings/unknownZoneRejectedWithoutMutation", unchanged, saved); err != nil {
		return err
	}
	if _, err := s.input.Client.restart(s.ctx, "auth"); err != nil {
		return err
	}
	after, err := s.org(0)
	if err != nil {
		return err
	}
	foreignAfter, err := e34HTTP(s.ctx, foreign, "organization", foreignParams, nil, nil, nil, 200)
	if err != nil {
		return err
	}
	report, err := s.report(0, "analyticsmovements", url.Values{"fromDate": {"2026-03-08"}, "throughDate": {"2026-03-08"}})
	if err != nil {
		return err
	}
	return errors.Join(e34Expect("/settings/restartPreserved", after, saved), e34Expect("/settings/otherOrgUnchanged", foreignAfter, foreignBefore), e34Expect("/settings/restartPreserved", e34Field(report, "context", "settings"), saved["settings"]), e34Expect("/settings/restartPreserved", reflect.DeepEqual(e34Field(report, "page", "settings"), saved["settings"]), true))
}
