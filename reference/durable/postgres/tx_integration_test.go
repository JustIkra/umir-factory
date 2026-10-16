//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/JustIkra/umir-factory/reference/durable/itest"
	"github.com/JustIkra/umir-factory/reference/durable/postgres"
)

func plainDB(t *testing.T, mutate func(*postgres.Config)) (*postgres.DB, string) {
	t.Helper()
	dbURL := itest.NewDatabase(t)
	return openDB(t, dbURL, mutate), dbURL
}

func exec(t testing.TB, db *postgres.DB, sql string) {
	t.Helper()
	err := db.InTx(context.Background(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sql)
		return err
	})
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func TestInTxRetriesSerializationFailure(t *testing.T) {
	db, _ := plainDB(t, nil)
	exec(t, db, "CREATE TABLE acc (id int PRIMARY KEY)")

	var barrier sync.WaitGroup
	barrier.Add(2)
	var attempts atomic.Int32
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() {
			first := true
			errs[i] = db.InTx(t.Context(), pgx.TxOptions{IsoLevel: pgx.Serializable}, func(ctx context.Context, tx pgx.Tx) error {
				attempts.Add(1)
				var n int
				if err := tx.QueryRow(ctx, "SELECT count(*) FROM acc").Scan(&n); err != nil {
					return err
				}
				if first {
					first = false
					barrier.Done()
					barrier.Wait() // both transactions have read before either writes
				}
				_, err := tx.Exec(ctx, "INSERT INTO acc (id) VALUES ($1)", i+1)
				return err
			})
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("transaction %d failed: %v", i, err)
		}
	}
	// One failure and one retry make three runs; a further conflict between the
	// retries would add runs, up to the limit of three attempts for each side.
	if got := attempts.Load(); got < 3 || got > 6 {
		t.Fatalf("attempts = %d, want 3..6 (a serialization failure was retried)", got)
	}
	if got := count(t, t.Context(), db, "SELECT count(*) FROM acc"); got != 2 {
		t.Fatalf("rows = %d, want 2", got)
	}
}

func TestInTxRetriesDeadlock(t *testing.T) {
	db, _ := plainDB(t, nil)
	exec(t, db, "CREATE TABLE locks (id int PRIMARY KEY); INSERT INTO locks VALUES (1), (2)")

	var barrier sync.WaitGroup
	barrier.Add(2)
	var attempts atomic.Int32
	var wg sync.WaitGroup
	errs := make([]error, 2)
	order := [2][2]int{{1, 2}, {2, 1}}
	for i := range 2 {
		wg.Go(func() {
			first := true
			errs[i] = db.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
				attempts.Add(1)
				if _, err := tx.Exec(ctx, "SELECT 1 FROM locks WHERE id = $1 FOR UPDATE", order[i][0]); err != nil {
					return err
				}
				if first {
					first = false
					barrier.Done()
					barrier.Wait() // each holds one lock and wants the other
				}
				_, err := tx.Exec(ctx, "SELECT 1 FROM locks WHERE id = $1 FOR UPDATE", order[i][1])
				return err
			})
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("transaction %d failed: %v", i, err)
		}
	}
	if got := attempts.Load(); got < 3 || got > 6 {
		t.Fatalf("attempts = %d, want 3..6 (the deadlock victim was retried)", got)
	}
}

func TestInTxGivesUpAfterThreeAttempts(t *testing.T) {
	db, _ := plainDB(t, nil)
	var attempts int
	err := db.InTx(t.Context(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error {
		attempts++
		return &pgconn.PgError{Code: "40001"}
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "40001" {
		t.Fatalf("error = %v, want the 40001 error", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}

	attempts = 0
	boom := errors.New("business failure")
	if err := db.InTx(t.Context(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error {
		attempts++
		return boom
	}); !errors.Is(err, boom) || attempts != 1 {
		t.Fatalf("other errors must not be retried: err=%v attempts=%d", err, attempts)
	}
}

func TestInTxStopsRetryingWhenContextIsDone(t *testing.T) {
	db, _ := plainDB(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	var attempts int
	err := db.InTx(ctx, pgx.TxOptions{}, func(context.Context, pgx.Tx) error {
		attempts++
		cancel()
		return &pgconn.PgError{Code: "40P01"}
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "40P01" {
		t.Fatalf("err = %v, want the deadlock error that was being retried", err)
	}
	var unknown *postgres.CommitUnknownError
	if errors.As(err, &unknown) {
		t.Fatalf("a failed transaction is not CommitUnknownError: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestInTxContextDoneBeforeCommitRollsBackWithoutUnknownOutcome(t *testing.T) {
	db, _ := plainDB(t, nil)
	exec(t, db, "CREATE TABLE early_cancel (id int PRIMARY KEY)")
	ctx, cancel := context.WithCancel(t.Context())
	var attempts int
	err := db.InTx(ctx, pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		attempts++
		if _, err := tx.Exec(ctx, "INSERT INTO early_cancel (id) VALUES (1)"); err != nil {
			return err
		}
		cancel() // fn succeeds, but the context is done before COMMIT is sent
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	var unknown *postgres.CommitUnknownError
	if errors.As(err, &unknown) {
		t.Fatalf("COMMIT was never sent, so the outcome is not unknown: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if got := count(t, t.Context(), db, "SELECT count(*) FROM early_cancel"); got != 0 {
		t.Fatalf("rows = %d, the transaction must have been rolled back", got)
	}
}

func TestInTxPausesBetweenRetries(t *testing.T) {
	db, _ := plainDB(t, nil)
	var times []time.Time
	err := db.InTx(t.Context(), pgx.TxOptions{}, func(context.Context, pgx.Tx) error {
		times = append(times, time.Now())
		return &pgconn.PgError{Code: "40001"}
	})
	if err == nil || len(times) != 3 {
		t.Fatalf("err=%v attempts=%d, want an error after 3 attempts", err, len(times))
	}
	for i := 1; i < len(times); i++ {
		// 5 ms is the shortest pause; allow for timer granularity.
		if gap := times[i].Sub(times[i-1]); gap < 4*time.Millisecond {
			t.Fatalf("attempts %d and %d are %v apart, want a pause of at least 5 ms", i, i+1, gap)
		}
	}
}

func proxiedDB(t *testing.T, dbURL string) (*postgres.DB, *itest.TCPProxy) {
	t.Helper()
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	host := u.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "5432")
	}
	proxy := itest.NewTCPProxy(t, host)
	u.Host = proxy.Addr()
	return openDB(t, u.String(), withMaxConns(1)), proxy
}

func TestInTxReportsCommitUnknownWhenConnectionIsCutRightAfterCommit(t *testing.T) {
	direct, dbURL := plainDB(t, nil)
	exec(t, direct, "CREATE TABLE commit_probe (id int PRIMARY KEY)")
	proxied, proxy := proxiedDB(t, dbURL)

	// pgx sends COMMIT as the simple query "commit" with its terminating NUL.
	proxy.CutAfterMatch(itest.ToServer, []byte("commit\x00"))
	var attempts int
	err := proxied.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		attempts++
		_, err := tx.Exec(ctx, "INSERT INTO commit_probe (id) VALUES (1)")
		return err
	})
	var unknown *postgres.CommitUnknownError
	if !errors.As(err, &unknown) {
		t.Fatalf("error = %v, want *CommitUnknownError", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d: an unknown commit must not be retried", attempts)
	}
	// The server did receive COMMIT, so the row is there although the caller
	// could not know.
	deadline := time.Now().Add(5 * time.Second)
	for count(t, t.Context(), direct, "SELECT count(*) FROM commit_probe") != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the committed row never became visible")
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The pool recovers on a new connection.
	if err := proxied.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO commit_probe (id) VALUES (2)")
		return err
	}); err != nil {
		t.Fatalf("the pool did not recover after the cut: %v", err)
	}
}

func TestInTxCutBeforeCommitIsNotCommitUnknown(t *testing.T) {
	direct, dbURL := plainDB(t, nil)
	exec(t, direct, "CREATE TABLE cut_probe (id int PRIMARY KEY)")
	proxied, proxy := proxiedDB(t, dbURL)

	proxy.CutAfterMatch(itest.ToServer, []byte("INSERT INTO cut_probe"))
	err := proxied.InTx(t.Context(), pgx.TxOptions{}, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO cut_probe (id) VALUES (1)")
		return err
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	var unknown *postgres.CommitUnknownError
	if errors.As(err, &unknown) {
		t.Fatalf("the failure happened before COMMIT, not CommitUnknownError: %v", err)
	}
	if got := count(t, t.Context(), direct, "SELECT count(*) FROM cut_probe"); got != 0 {
		t.Fatalf("rows = %d, the transaction must not have committed", got)
	}
}
