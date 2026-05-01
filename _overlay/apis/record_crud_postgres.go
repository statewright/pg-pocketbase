//go:build postgres

package apis

// defaultCountCol uses the primary key column for PostgreSQL (no implicit rowid).
const defaultCountCol = "id"
