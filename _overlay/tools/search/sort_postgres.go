//go:build postgres

package search

// rowidSortExpr is the SQL expression used for @rowid sorting in PostgreSQL.
// Maps to the primary key since PG has no implicit rowid.
const rowidSortExpr = "[[id]]"
