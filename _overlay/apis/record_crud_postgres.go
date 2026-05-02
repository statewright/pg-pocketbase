//go:build postgres

package apis

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/pocketbase/pocketbase/tools/types"
)

// defaultCountCol uses the primary key column for PostgreSQL (no implicit rowid).
const defaultCountCol = "id"

// sanitizeRuleParam converts values that the pgx driver cannot encode
// into PostgreSQL's default text parameter format.
//
// When PocketBase builds a CTE for create-rule evaluation, every record
// field value is bound as a prepared-statement parameter.  PostgreSQL
// infers all untyped parameter slots as text (OID 25).  pgx cannot
// encode non-string Go types into text format, producing errors like:
//
//	"unable to encode false into text format for text (OID 25)"
//	"unable to encode 1 into text format for text (OID 25)"
//
// Converting all non-string types to their string representation lets
// PostgreSQL accept them.  The corresponding sanitizeRuleSelectExpr
// adds explicit type casts so PostgreSQL can compare the string-encoded
// values against their native column types.
func sanitizeRuleParam(v any) any {
	switch val := v.(type) {
	case nil:
		return nil
	case string:
		return val
	case bool:
		if val {
			return "true"
		}
		return "false"
	case int:
		return strconv.FormatInt(int64(val), 10)
	case int8:
		return strconv.FormatInt(int64(val), 10)
	case int16:
		return strconv.FormatInt(int64(val), 10)
	case int32:
		return strconv.FormatInt(int64(val), 10)
	case int64:
		return strconv.FormatInt(val, 10)
	case uint:
		return strconv.FormatUint(uint64(val), 10)
	case uint8:
		return strconv.FormatUint(uint64(val), 10)
	case uint16:
		return strconv.FormatUint(uint64(val), 10)
	case uint32:
		return strconv.FormatUint(uint64(val), 10)
	case uint64:
		return strconv.FormatUint(val, 10)
	case float32:
		return strconv.FormatFloat(float64(val), 'f', -1, 32)
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case types.DateTime:
		return val.String()
	case types.JSONRaw:
		// JSONRaw is []byte; pgx cannot encode []byte for text OID.
		return val.String()
	case time.Time:
		return val.UTC().Format("2006-01-02 15:04:05.000Z")
	case json.RawMessage:
		return string(val)
	default:
		// For any other type that implements fmt.Stringer or driver.Valuer,
		// attempt JSON marshaling as a last resort (covers maps, slices, etc.).
		b, err := json.Marshal(val)
		if err != nil {
			return fmt.Sprintf("%v", val)
		}
		return string(b)
	}
}

// sanitizeRuleSelectExpr returns the SELECT expression for a CTE column,
// adding an explicit type cast when needed so PostgreSQL can round-trip
// the text parameter back to the column's native type.
func sanitizeRuleSelectExpr(paramPlaceholder, columnAlias string, v any) string {
	cast := inferCast(v)
	if cast != "" {
		return fmt.Sprintf("{:%s}::%s AS [[%s]]", paramPlaceholder, cast, columnAlias)
	}
	return fmt.Sprintf("{:%s} AS [[%s]]", paramPlaceholder, columnAlias)
}

// inferCast returns the PostgreSQL type cast suffix needed for the given
// Go value, or "" if no cast is needed (string, nil).
func inferCast(v any) string {
	switch v.(type) {
	case bool:
		return "boolean"
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return "numeric"
	case float32, float64:
		return "numeric"
	case types.JSONRaw, json.RawMessage:
		return "jsonb"
	case types.DateTime:
		// PocketBase stores dates as text in a fixed format;
		// an explicit cast is not needed since the string representation
		// is already comparable.  However, casting to timestamptz
		// ensures type consistency if the column is typed.
		return "timestamptz"
	case time.Time:
		return "timestamptz"
	default:
		return ""
	}
}
