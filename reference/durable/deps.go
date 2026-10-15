// Package durable is the root of the reference module for the durable stack.
//
// Temporary scaffold: deps.go pins the module dependencies so that the packages
// written in parallel do not touch go.mod and go.sum. The lead removes this
// file together with the final `go mod tidy`.
package durable

import (
	_ "github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/pgtype"
	_ "github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/rabbitmq/amqp091-go"
	_ "github.com/redis/go-redis/v9"
)
