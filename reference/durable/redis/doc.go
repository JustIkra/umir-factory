// Package redis is the reference Redis adapter of the durable stack: the
// admission limiter, the continuity marker, Gateway leases and counted slots.
//
// Services copy the package to internal/adapters/redis without changing its
// logic. The package imports only the standard library and go-redis.
//
// # Keys
//
// Every key has the form <root>umir:<owner>:<kind>:..., where owner (gw, wh)
// is chosen by the caller, kind and the rest come from the operation, and root
// is the optional Config.KeyRoot (empty in production, the itest prefix in
// tests). The continuity marker lives under umir:limiter:, shared by services.
//
//	umir:<owner>:adm:<dimension key>          fixed-window counter of one dimension
//	umir:<owner>:admid:<policy>:<sha256(ID)>  admission record, holds sha256(fingerprint)
//	umir:<owner>:<name>:lease, :fence         Lease
//	umir:<owner>:<slot key>, <slot key>:u     Slots (ZSET of holders, HASH of units)
//	umir:limiter:generation, strict_since     Continuity
//
// Scripts touch several keys at once, so the package targets a single Redis
// node (one cell), not Redis Cluster.
//
// # Failure model
//
// Admission never answers "allowed" without a verified reply from Redis. Any
// Redis error, timeout, canceled context or unparsable reply from
// Limiter.Admit or Continuity.Check is returned as ErrAdmissionUnavailable.
// The client does not retry commands on its own: a lost reply is resolved by
// the caller repeating the request with the same ID, which is charged once.
package redis
