//go:build !postgres

package apis

import "fmt"

// defaultCountCol uses SQLite's implicit _rowid_ to minimize the need of a covering index.
const defaultCountCol = "_rowid_"

// sanitizeRuleParam is a no-op for SQLite — all Go types encode safely.
func sanitizeRuleParam(v any) any { return v }

// sanitizeRuleSelectExpr returns the SELECT expression for a CTE column.
// SQLite needs no type casts.
func sanitizeRuleSelectExpr(paramPlaceholder, columnAlias string, _ any) string {
	return fmt.Sprintf("{:%s} AS [[%s]]", paramPlaceholder, columnAlias)
}
