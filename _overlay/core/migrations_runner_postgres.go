//go:build postgres

package core

// migrationsAppliedColumnType is BIGINT for PostgreSQL (UnixMicro overflows int4).
const migrationsAppliedColumnType = "BIGINT"
