// Package postgres is the reference PostgreSQL adapter of the durable stack: the
// pool, transactions that retry and carry the row-level security context,
// migrations, the storage generation, worker leases, the transactional outbox
// and the inbox. Services copy it to internal/adapters/postgres.
//
// Roles. Each service has an owner role (umir_<svc>_owner, owns the tables, used
// only by the migrate job) and a runtime role (umir_<svc>, not the owner,
// NOBYPASSRLS). The service calls AssertRuntimeRole at start. Runtime
// transactions that touch tenant data use InScopedTx or InSystemTx, which set
// the umir.* settings with set_config(..., true), so nothing outlives the
// transaction on a pooled connection.
//
// Time. Leases, outbox claims and retention compare timestamps. When
// Config.Clock is nil they use the database clock (clock_timestamp()), the one
// time source shared by all replicas; tests set Config.Clock to move time
// without sleeping.
//
// SQL fragments. StorageMetaSQL, WorkerLeasesSQL, OutboxSQL and InboxSQL return
// the reference DDL. A service migration includes one with the line
//
//	-- umir:include <storage_meta|worker_leases|outbox|inbox>
//
// which Migrate and VerifySchema replace by the fragment text.
package postgres
