package main

import (
	"errors"
	"net/url"
	"time"

	"github.com/JustIkra/umir-factory/tests/http/internal/faultproxy"
)

func e34Calendar(s *e34Scenario) error {
	if err := s.zone(0, "Europe/Berlin"); err != nil {
		return err
	}
	for _, test := range []struct {
		date, from, to, pointer string
		hours                   time.Duration
	}{
		{"2026-03-29", "2026-03-29T00:00:00+01:00", "2026-03-30T00:00:00+02:00", "/calendar/shortDayHours", 23 * time.Hour},
		{"2026-10-25", "2026-10-25T00:00:00+02:00", "2026-10-26T00:00:00+01:00", "/calendar/longDayHours", 25 * time.Hour},
	} {
		report, err := s.report(0, "analyticsmovements", url.Values{"fromDate": {test.date}, "throughDate": {test.date}})
		if err != nil {
			return err
		}
		duration, err := e34Duration(report, "from", "to")
		if err != nil {
			return err
		}
		if err := errors.Join(e34Expect(test.pointer, duration, test.hours), e34Expect(test.pointer, e34SameInstant(report["from"], test.from), true), e34Expect(test.pointer, e34SameInstant(report["to"], test.to), true)); err != nil {
			return err
		}
	}
	if err := s.zone(0, "Pacific/Apia"); err != nil {
		return err
	}
	org, err := s.org(0)
	if err != nil {
		return err
	}
	version, ok := e34Field(org, "settings", "settingsVersion").(string)
	if !ok || version == "" {
		return errors.New("calendar settings missing")
	}
	for _, day := range []string{"2011-12-30", "2011-12-29"} {
		refused, err := s.call(0, "analyticsmovements", url.Values{"fromDate": {day}, "throughDate": {day}, "expectedSettingsVersion": {version}}, nil, nil, 422)
		if err != nil {
			return err
		}
		if err := e34Expect("/calendar/skippedDateError", refused["code"], "SkippedDate"); err != nil {
			return err
		}
	}
	for _, test := range []struct{ zone, date, from, to string }{
		{"America/Havana", "2016-11-06", "2016-11-06T00:00:00-04:00", "2016-11-07T00:00:00-05:00"},
		{"America/Sao_Paulo", "2018-11-04", "2018-11-04T01:00:00-02:00", "2018-11-05T00:00:00-02:00"},
	} {
		if err := s.zone(0, test.zone); err != nil {
			return err
		}
		report, err := s.report(0, "analyticsmovements", url.Values{"fromDate": {test.date}, "throughDate": {test.date}})
		if err != nil {
			return err
		}
		if err := errors.Join(e34Expect("/calendar/rareMidnightPolicyValid", e34SameInstant(report["from"], test.from), true), e34Expect("/calendar/rareMidnightPolicyValid", e34SameInstant(report["to"], test.to), true)); err != nil {
			return err
		}
	}
	return nil
}

func (s *e34Scenario) advanceAndLogin(instant time.Time) error {
	if err := s.input.Client.advance(s.ctx, instant.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	password, err := s.input.Client.credential("bootstrap-password")
	if err != nil {
		return err
	}
	defer clear(password)
	command, err := e34ID()
	if err != nil {
		return err
	}
	_, err = s.input.Session.loginAs(s.ctx, "e2e.initial", password, command, false)
	return err
}

func e34Civil(s *e34Scenario) error {
	if err := s.zone(0, "Europe/Berlin"); err != nil {
		return err
	}
	if err := s.product(0, "civil", "Civil", "g"); err != nil {
		return err
	}
	now, err := s.clock()
	if err != nil {
		return err
	}
	minimum, err := time.Parse(time.RFC3339, "2026-10-25T04:00:00+01:00")
	if err != nil {
		return err
	}
	if now.Before(minimum) {
		if err := s.advanceAndLogin(minimum); err != nil {
			return err
		}
		now, err = s.clock()
		if err != nil {
			return err
		}
	}
	before, err := s.events(0)
	if err != nil {
		return err
	}
	for _, test := range []struct {
		civil         map[string]any
		code, pointer string
	}{
		{map[string]any{"localDateTime": "2026-10-25T02:30:00"}, "AmbiguousLocalTime", "/time/ambiguousError"},
		{map[string]any{"localDateTime": "2026-03-29T02:30:00"}, "NonexistentLocalTime", "/time/gapError"},
		{map[string]any{"localDateTime": "2026-10-25T02:30:00", "offset": "+02:00", "occurrence": "later"}, "InvalidArgument", "/time/conflictingChoiceRejected"},
	} {
		refused, err := s.delivery(0, []any{e34Line("civil", "1000", map[string]any{"civil": test.civil}, e34Instant(now), "1", false)}, e34Instant(now), 422, 400)
		if err != nil {
			return err
		}
		if err := e34Expect(test.pointer, refused["code"], test.code); err != nil {
			return err
		}
	}
	afterInvalid, err := s.events(0)
	if err != nil {
		return err
	}
	if err := e34Expect("/journal/invalidInputFacts", afterInvalid, before); err != nil {
		return err
	}
	for _, test := range []struct {
		input   map[string]any
		instant string
	}{
		{map[string]any{"civil": map[string]any{"localDateTime": "2026-10-25T02:30:00", "occurrence": "earlier"}}, "2026-10-25T02:30:00+02:00"},
		{map[string]any{"civil": map[string]any{"localDateTime": "2026-10-25T02:30:00", "occurrence": "later"}}, "2026-10-25T02:30:00+01:00"},
		{map[string]any{"instant": "2026-03-28T12:00:00+09:00"}, "2026-03-28T12:00:00+09:00"},
	} {
		receipt, err := s.delivery(0, []any{e34Line("civil", "1000", test.input, e34Instant(now), "1", false)}, e34Instant(now), 200)
		if err != nil {
			return err
		}
		batch, err := e34Batch(receipt)
		if err != nil {
			return err
		}
		actual, ok := batch["manufacturedAt"].(string)
		if !ok {
			return errors.New("stored civil resolution missing")
		}
		resolved, err := time.Parse(time.RFC3339Nano, actual)
		wanted, wantedErr := time.Parse(time.RFC3339Nano, test.instant)
		if err != nil || wantedErr != nil {
			return errors.New("stored civil/explicit instant resolution differs")
		}
		if err := e34Expect("/time/explicitInstantOffsetAccepted", resolved.Equal(wanted), true); err != nil {
			return err
		}
	}
	return nil
}

func e34ShelfLife(s *e34Scenario) error {
	if err := s.product(0, "shelf", "Shelf life", "g"); err != nil {
		return err
	}
	now, err := s.clock()
	if err != nil {
		return err
	}
	reply, err := s.input.Client.privateCall(s.ctx, "warehouse", "warehouseRuntime", nil, nil, nil, backendPrivateCredential{Name: "auth-warehouse"}, 200)
	if err != nil {
		return err
	}
	e34Retain(s.ctx, "warehouseRuntime", nil, reply.Body, reply.Status)
	parsed, err := faultproxy.StrictJSON(reply.Body, 32)
	runtime, ok := parsed.(map[string]any)
	if err != nil || !ok || runtime["ready"] != true {
		return errors.New("real ready Warehouse runtime required")
	}
	tzdata, ok := runtime["tzdataVersion"].(string)
	if !ok || tzdata == "" {
		return errors.New("loaded owner tzdata version required")
	}
	for _, test := range []struct {
		zone, made, shelf, expires, pointer string
		months                              bool
	}{
		{"Europe/Moscow", "2026-01-31T12:00:00+03:00", "1", "2026-02-28T12:00:00+03:00", "/expiry/clamp", true},
		{"Europe/Berlin", "2026-09-25T02:30:00+02:00", "1", "2026-10-25T02:30:00+02:00", "/expiry/fold", true},
		{"Europe/Berlin", "2026-01-29T02:30:00+01:00", "2", "2026-03-29T03:00:00+02:00", "/expiry/gap", true},
		{"Europe/Berlin", "2026-03-28T12:00:00+01:00", "1", "2026-03-29T13:00:00+02:00", "/expiry/dayDurationHours", false},
	} {
		if err := s.zone(0, test.zone); err != nil {
			return err
		}
		org, err := s.org(0)
		if err != nil {
			return err
		}
		receipt, err := s.delivery(0, []any{e34Line("shelf", "1000", map[string]any{"instant": test.made}, e34Instant(now), test.shelf, test.months)}, e34Instant(now), 200)
		if err != nil {
			return err
		}
		batch, err := e34Batch(receipt)
		if err != nil {
			return err
		}
		if err := errors.Join(e34Expect(test.pointer, batch["expiresAt"], test.expires), e34Expect(test.pointer, batch["appliedSettings"], org["settings"]), e34Expect(test.pointer, batch["tzdataVersion"], tzdata)); err != nil {
			return err
		}
		if !test.months {
			duration, err := e34Duration(batch, "manufacturedAt", "expiresAt")
			if err != nil {
				return err
			}
			if err := e34Expect("/expiry/dayDurationHours", duration, 24*time.Hour); err != nil {
				return err
			}
		}
	}
	return nil
}
