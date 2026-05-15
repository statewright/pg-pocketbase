//go:build !postgres

package search

import "fmt"

// jsonExtractExpr returns the SQL expression for JSON field extraction.
// SQLite: uses JSON_EXTRACT directly (built-in function).
func jsonExtractExpr(column, jsonPath string) string {
	return fmt.Sprintf("JSON_EXTRACT([[%s]], '%s')", column, jsonPath)
}
