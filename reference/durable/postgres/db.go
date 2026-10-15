package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// maxTxAttempts bounds retries of serialization failures and deadlocks.
	maxTxAttempts = 3

	defaultApplicationName = "umir-service"
	defaultConnectTimeout  = 10 * time.Second
	rollbackTimeout        = 5 * time.Second

	sqlStateSerializationFailure = "40001"
	sqlStateDeadlockDetected     = "40P01"
)

// Config describes how to reach the database.
type Config struct {
	// URL is a libpq URL. Pool tuning may come from its query parameters.
	URL string
	// ApplicationName is reported to the server unless the URL already sets one.
	ApplicationName string
	// MaxConns caps the pool. Zero keeps pool_max_conns from the URL or, when
	// absent, the pgx default.
	MaxConns int32
	// ConnectTimeout bounds the initial connection check. Zero means 10 seconds.
	ConnectTimeout time.Duration
	// Clock is the time source of leases, outbox claims and retention. Nil uses
	// the database clock; tests inject a fake clock here.
	Clock func() time.Time
}

// DB is the shared connection pool.
type DB struct {
	pool  *pgxpool.Pool
	clock func() time.Time
}

// Open creates the pool and verifies connectivity. Errors never contain the URL.
func Open(ctx context.Context, cfg Config) (*DB, error) {
	if cfg.URL == "" {
		return nil, errors.New("database URL is empty")
	}
	pgxConfig, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		// The driver error can quote parts of the URL.
		return nil, errors.New("database URL is invalid")
	}
	if pgxConfig.ConnConfig.RuntimeParams["application_name"] == "" {
		name := cfg.ApplicationName
		if name == "" {
			name = defaultApplicationName
		}
		pgxConfig.ConnConfig.RuntimeParams["application_name"] = name
	}
	if cfg.MaxConns > 0 {
		pgxConfig.MaxConns = cfg.MaxConns
	}
	timeout := cfg.ConnectTimeout
	if timeout <= 0 {
		timeout = defaultConnectTimeout
	}
	pgxConfig.ConnConfig.ConnectTimeout = timeout
	pool, err := pgxpool.NewWithConfig(ctx, pgxConfig)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return &DB{pool: pool, clock: cfg.Clock}, nil
}

// Close releases every pooled connection.
func (d *DB) Close() { d.pool.Close() }

// Ping reports whether the database answers.
func (d *DB) Ping(ctx context.Context) error { return d.pool.Ping(ctx) }

// Pool exposes the pool for code that needs dedicated connections.
func (d *DB) Pool() *pgxpool.Pool { return d.pool }

// now is the "current time" argument of statements that compare timestamps: the
// injected clock, or nil so that the statement falls back to clock_timestamp().
func (d *DB) now() any {
	if d.clock == nil {
		return nil
	}
	return d.clock().UTC()
}

// CommitUnknownError reports that COMMIT was sent but its outcome is unknown,
// for example because the connection broke while waiting for the answer. The
// transaction may or may not have been applied; it is never retried.
type CommitUnknownError struct{ Err error }

func (e *CommitUnknownError) Error() string {
	return "transaction commit outcome unknown: " + e.Err.Error()
}

func (e *CommitUnknownError) Unwrap() error { return e.Err }

// InTx runs fn in a transaction. Serialization failures (40001) and deadlocks
// (40P01) are retried, up to three attempts in total, so fn must be safe to run
// again. When the connection fails while COMMIT is in flight, the error is a
// *CommitUnknownError. A panic in fn rolls the transaction back and propagates.
func (d *DB) InTx(ctx context.Context, opts pgx.TxOptions, fn func(context.Context, pgx.Tx) error) error {
	return d.run(ctx, opts, nil, fn)
}

// run is InTx with a hook that prepares every attempt, used to set the scope.
func (d *DB) run(ctx context.Context, opts pgx.TxOptions, prepare, fn func(context.Context, pgx.Tx) error) error {
	var err error
	for attempt := 1; attempt <= maxTxAttempts; attempt++ {
		err = d.attempt(ctx, opts, prepare, fn)
		if err == nil || !retryable(err) || ctx.Err() != nil {
			return err
		}
	}
	return err
}

func (d *DB) attempt(ctx context.Context, opts pgx.TxOptions, prepare, fn func(context.Context, pgx.Tx) error) error {
	tx, err := d.pool.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	finished := false
	defer func() {
		if finished {
			return
		}
		// fn or prepare panicked: free the connection before the panic goes on.
		rollback(ctx, tx)
	}()
	if prepare != nil {
		if err := prepare(ctx, tx); err != nil {
			finished = true
			rollback(ctx, tx)
			return err
		}
	}
	if err := fn(ctx, tx); err != nil {
		finished = true
		rollback(ctx, tx)
		return err
	}
	finished = true
	if err := tx.Commit(ctx); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) || errors.Is(err, pgx.ErrTxClosed) || errors.Is(err, pgx.ErrTxCommitRollback) {
			return fmt.Errorf("commit transaction: %w", err)
		}
		return &CommitUnknownError{Err: err}
	}
	return nil
}

// rollback runs on a fresh context so that it proceeds after ctx is cancelled.
// Its error is dropped on purpose: the caller reports the original failure, and a
// connection that cannot roll back is destroyed by the pool.
func rollback(ctx context.Context, tx pgx.Tx) {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	if err := tx.Rollback(rollbackCtx); err != nil {
		return
	}
}

func retryable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == sqlStateSerializationFailure || pgErr.Code == sqlStateDeadlockDetected
}
