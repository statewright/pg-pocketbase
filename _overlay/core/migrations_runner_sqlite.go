//go:build !postgres

package core

// migrationsAppliedColumnType is INTEGER for SQLite (8-byte integer by default).
const migrationsAppliedColumnType = "INTEGER"
