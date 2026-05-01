//go:build !postgres

package core

// defaultCollectionsSortExpr uses SQLite's implicit rowid for insertion-order sorting.
const defaultCollectionsSortExpr = "rowid ASC"
