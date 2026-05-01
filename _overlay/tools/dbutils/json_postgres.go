//go:build postgres

package dbutils

import (
	"fmt"
	"strings"

	"github.com/pocketbase/dbx"
)

// JSONEach returns a PostgreSQL jsonb_array_elements_text expression with
// normalizations for non-json columns.
func JSONEach(column string) string {
	return fmt.Sprintf(
		`jsonb_array_elements_text(CASE WHEN ([[%s]] IS JSON OR json_valid([[%s]]::text)) AND jsonb_typeof([[%s]]::jsonb) = 'array' THEN [[%s]]::jsonb ELSE jsonb_build_array([[%s]]) END)`,
		column, column, column, column, column,
	)
}

// JSONEachByPlaceholder expands a given user input json array to multiple rows.
// The placeholder is the parameter placeholder in SQL prepared statements.
func JSONEachByPlaceholder(placeholder string) string {
	return fmt.Sprintf(
		`jsonb_array_elements({:%s}::jsonb)`,
		placeholder,
	)
}

// JsonArrayExistsStr checks whether a JSON string array contains a string element
// using PostgreSQL's ? operator.
func JsonArrayExistsStr(column string, strValue string) dbx.Expression {
	return dbx.NewExp(fmt.Sprintf("[[%s]] ? {:value}::text", column), dbx.Params{
		"value": strValue,
	})
}

// JSONArrayLength returns a PostgreSQL jsonb_array_length expression
// with normalizations for non-json columns.
//
// Returns 0 for empty string or NULL column values.
func JSONArrayLength(column string) string {
	return fmt.Sprintf(
		`(CASE WHEN ([[%s]] IS JSON OR JSON_VALID([[%s]]::text)) AND jsonb_typeof([[%s]]::jsonb) = 'array' THEN jsonb_array_length([[%s]]::jsonb) ELSE 0 END)`,
		column, column, column, column,
	)
}

// JSONExtract returns a PostgreSQL JSON_QUERY_OR_NULL expression with
// normalizations for non-json columns.
func JSONExtract(column string, path string) string {
	if path != "" && !strings.HasPrefix(path, "[") {
		path = "." + path
	}

	return fmt.Sprintf(
		`JSON_QUERY_OR_NULL([[%s]], '$%s')::jsonb`,
		column,
		path,
	)
}
