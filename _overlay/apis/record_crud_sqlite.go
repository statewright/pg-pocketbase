//go:build !postgres

package apis

// defaultCountCol uses SQLite's implicit _rowid_ to minimize the need of a covering index.
const defaultCountCol = "_rowid_"
