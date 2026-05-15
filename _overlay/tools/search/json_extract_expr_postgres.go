//go:build postgres

package search

import "fmt"

// jsonExtractExpr returns the SQL expression for JSON field extraction.
// PostgreSQL: appends ::jsonb so the filter builder recognizes the type
// and properly casts comparison operands.
func jsonExtractExpr(column, jsonPath string) string {
	return fmt.Sprintf("JSON_EXTRACT([[%s]], '%s')::jsonb", column, jsonPath)
}
