package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestOpenErrorsDoNotLeakCredentials(t *testing.T) {
	cases := map[string]string{
		"malformed":   "postgres://svc:s3cr3t-pass@db:notaport/app",
		"unreachable": "postgres://svc:s3cr3t-pass@127.0.0.1:1/app?sslmode=disable",
	}
	for name, url := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			db, err := Open(ctx, Config{URL: url, ConnectTimeout: 2 * time.Second})
			if err == nil {
				db.Close()
				t.Fatal("Open must fail")
			}
			if strings.Contains(err.Error(), "s3cr3t-pass") {
				t.Fatalf("error leaks the password: %v", err)
			}
		})
	}
}

func TestOpenRejectsEmptyURL(t *testing.T) {
	if _, err := Open(t.Context(), Config{}); err == nil {
		t.Fatal("an empty URL must be rejected")
	}
}

func TestRetryable(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("plain"), false},
		{&pgconn.PgError{Code: "40001"}, true},
		{&pgconn.PgError{Code: "40P01"}, true},
		{fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "40001"}), true},
		{&pgconn.PgError{Code: "23505"}, false},
		{&CommitUnknownError{Err: errors.New("conn closed")}, false},
	}
	for _, tc := range cases {
		if got := retryable(tc.err); got != tc.want {
			t.Errorf("retryable(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestCommitUnknownErrorUnwraps(t *testing.T) {
	cause := errors.New("conn closed")
	err := fmt.Errorf("outer: %w", &CommitUnknownError{Err: cause})
	var unknown *CommitUnknownError
	if !errors.As(err, &unknown) || !errors.Is(err, cause) {
		t.Fatalf("CommitUnknownError must be found by errors.As and unwrap to its cause: %v", err)
	}
}

func TestRetryPausesWithJitterBetweenAttempts(t *testing.T) {
	var pauses []time.Duration
	d := &DB{sleep: func(_ context.Context, delay time.Duration) error {
		pauses = append(pauses, delay)
		return nil
	}}
	calls := 0
	err := d.retry(t.Context(), func() error {
		calls++
		return &pgconn.PgError{Code: "40001"}
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || calls != maxTxAttempts {
		t.Fatalf("err=%v calls=%d, want the 40001 error after %d calls", err, calls, maxTxAttempts)
	}
	if len(pauses) != maxTxAttempts-1 {
		t.Fatalf("pauses = %v, want one between each pair of attempts and none after the last", pauses)
	}
	for _, p := range pauses {
		if p < retryPauseMin || p > retryPauseMax {
			t.Fatalf("pause %v is outside [%v, %v]", p, retryPauseMin, retryPauseMax)
		}
	}
}

func TestRetryDoesNotPauseWithoutRetry(t *testing.T) {
	d := &DB{sleep: func(context.Context, time.Duration) error {
		t.Error("no pause expected")
		return nil
	}}
	if err := d.retry(t.Context(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("business")
	if err := d.retry(t.Context(), func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestRetryStopsWhenContextEndsDuringThePause(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	d := &DB{} // the real timer pause
	calls := 0
	go func() {
		time.Sleep(retryPauseMin / 5)
		cancel()
	}()
	start := time.Now()
	err := d.retry(ctx, func() error {
		calls++
		return &pgconn.PgError{Code: "40P01"}
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "40P01" {
		t.Fatalf("err = %v, want the deadlock error that was being retried", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1: the context ended during the first pause", calls)
	}
	if time.Since(start) > time.Second {
		t.Fatal("the pause did not end with the context")
	}
}

func TestSleepContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := sleepContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if err := sleepContext(t.Context(), time.Millisecond); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestCommitOutcomeUnknown(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"broken connection", errors.New("write: broken pipe"), true},
		{"context ended while waiting", context.Canceled, true},
		{"deadline while waiting", fmt.Errorf("commit: %w", context.DeadlineExceeded), true},
		{"deferred constraint", &pgconn.PgError{Code: "23505", Severity: "ERROR"}, false},
		{"serialization failure", &pgconn.PgError{Code: "40001", Severity: "ERROR"}, false},
		{"FATAL error", &pgconn.PgError{Code: "XX000", Severity: "FATAL"}, true},
		{"FATAL unlocalized", &pgconn.PgError{Code: "XX000", Severity: "ФАТАЛЬНО", SeverityUnlocalized: "FATAL"}, true},
		{"PANIC error", &pgconn.PgError{Code: "XX000", Severity: "PANIC"}, true},
		{"connection exception class", &pgconn.PgError{Code: "08006", Severity: "ERROR"}, true},
		{"admin shutdown class", &pgconn.PgError{Code: "57P01", Severity: "ERROR"}, true},
		{"query canceled class", &pgconn.PgError{Code: "57014", Severity: "ERROR"}, true},
		{"transaction already closed", pgx.ErrTxClosed, false},
		{"commit became rollback", pgx.ErrTxCommitRollback, false},
	}
	for _, tc := range cases {
		if got := commitOutcomeUnknown(tc.err); got != tc.want {
			t.Errorf("%s: commitOutcomeUnknown = %v, want %v", tc.name, got, tc.want)
		}
	}
}
