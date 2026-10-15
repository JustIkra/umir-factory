package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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
