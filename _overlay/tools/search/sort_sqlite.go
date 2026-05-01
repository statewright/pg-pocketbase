//go:build !postgres

package search

// rowidSortExpr is the SQL expression used for @rowid sorting in SQLite.
const rowidSortExpr = "[[_rowid_]]"
