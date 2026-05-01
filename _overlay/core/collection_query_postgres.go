//go:build postgres

package core

// defaultCollectionsSortExpr uses the primary key for deterministic ordering in PostgreSQL.
const defaultCollectionsSortExpr = "id ASC"
