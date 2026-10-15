package postgres

import (
	_ "embed"
	"strings"
)

//go:embed storage_meta.sql
var storageMetaSQL string

//go:embed worker_leases.sql
var workerLeasesSQL string

//go:embed outbox.sql
var outboxSQL string

//go:embed inbox.sql
var inboxSQL string

// StorageMetaSQL is the DDL of storage_meta, including the first generation.
func StorageMetaSQL() string { return storageMetaSQL }

// WorkerLeasesSQL is the DDL of worker_leases.
func WorkerLeasesSQL() string { return workerLeasesSQL }

// OutboxSQL is the DDL of outbox and of its claim fence sequence.
func OutboxSQL() string { return outboxSQL }

// InboxSQL is the DDL of inbox.
func InboxSQL() string { return inboxSQL }

const includePrefix = "-- umir:include "

// fragment returns the DDL for an include directive name.
func fragment(name string) (string, bool) {
	switch name {
	case "storage_meta":
		return storageMetaSQL, true
	case "worker_leases":
		return workerLeasesSQL, true
	case "outbox":
		return outboxSQL, true
	case "inbox":
		return inboxSQL, true
	}
	return "", false
}

// expandIncludes replaces every line "-- umir:include <name>" by the fragment.
// The second result names an unknown fragment.
func expandIncludes(sql string) (string, string) {
	if !strings.Contains(sql, includePrefix) {
		return sql, ""
	}
	lines := strings.Split(sql, "\n")
	for i, line := range lines {
		rest, ok := strings.CutPrefix(strings.TrimRight(line, "\r \t"), includePrefix)
		if !ok {
			continue
		}
		text, known := fragment(strings.TrimSpace(rest))
		if !known {
			return "", strings.TrimSpace(rest)
		}
		lines[i] = strings.TrimRight(text, "\n")
	}
	return strings.Join(lines, "\n"), ""
}
