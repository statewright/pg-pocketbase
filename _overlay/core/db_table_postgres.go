//go:build postgres

package core

import (
	"fmt"
	"strings"

	"github.com/pocketbase/dbx"
)

// TableColumns returns all column names of a single table by its name.
func (app *BaseApp) TableColumns(tableName string) ([]string, error) {
	columns := []string{}

	err := app.ConcurrentDB().NewQuery(`
		SELECT column_name
		FROM information_schema.columns
		WHERE table_name = {:tableName}
		  AND table_schema = current_schema()
	`).Bind(dbx.Params{"tableName": tableName}).Column(&columns)

	return columns, err
}

// TableInfo returns table column information for the specified table.
func (app *BaseApp) TableInfo(tableName string) ([]*TableInfoRow, error) {
	info := []*TableInfoRow{}

	err := app.ConcurrentDB().NewQuery(`
		SELECT
			ordinal_position - 1 AS cid,
			c.column_name AS name,
			data_type AS type,
			CASE WHEN is_nullable = 'NO' THEN 1 ELSE 0 END AS notnull,
			column_default AS dflt_value,
			CASE WHEN pk.constraint_type = 'PRIMARY KEY' THEN 1 ELSE 0 END AS pk
		FROM information_schema.columns c
		LEFT JOIN (
			SELECT ccu.column_name, tc.constraint_type
			FROM information_schema.table_constraints tc
			JOIN information_schema.constraint_column_usage ccu
				ON tc.constraint_name = ccu.constraint_name
			WHERE tc.constraint_type = 'PRIMARY KEY'
				AND tc.table_name = {:tableName}
				AND tc.table_schema = current_schema()
		) pk ON c.column_name = pk.column_name
		WHERE c.table_name = {:tableName}
			AND c.table_schema = current_schema()
		ORDER BY c.ordinal_position
	`).Bind(dbx.Params{"tableName": tableName}).All(&info)
	if err != nil {
		return nil, err
	}

	if len(info) == 0 {
		return nil, fmt.Errorf("empty table info probably due to invalid or missing table %s", tableName)
	}

	return info, nil
}

// TableIndexes returns a name grouped map with all non empty index of the specified table.
//
// Note: This method doesn't return an error on nonexisting table.
func (app *BaseApp) TableIndexes(tableName string) (map[string]string, error) {
	indexes := []struct {
		Name string `db:"indexname"`
		Sql  string `db:"indexdef"`
	}{}

	err := app.ConcurrentDB().NewQuery(`
		SELECT indexname, indexdef
		FROM pg_indexes
		WHERE tablename = {:tableName}
		AND indexname NOT IN (
			SELECT conname
			FROM pg_constraint
			WHERE contype = 'p' AND conrelid = {:tableName}::regclass
		)
	`).Bind(dbx.Params{"tableName": tableName}).All(&indexes)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string, len(indexes))

	for _, idx := range indexes {
		result[idx.Name] = idx.Sql
	}

	return result, nil
}

// DeleteTable drops the specified table.
//
// This method is a no-op if a table with the provided name doesn't exist.
// Uses CASCADE to drop dependent objects (views, etc.) in PostgreSQL.
//
// NB! Be aware that this method is vulnerable to SQL injection and the
// "dangerousTableName" argument must come only from trusted input!
func (app *BaseApp) DeleteTable(dangerousTableName string) error {
	if strings.TrimSpace(dangerousTableName) == "" {
		return fmt.Errorf("invalid table name")
	}
	_, err := app.NonconcurrentDB().NewQuery(fmt.Sprintf(
		"DROP TABLE IF EXISTS {{%s}} CASCADE",
		dangerousTableName,
	)).Execute()

	return err
}

// HasTable checks if a table (or view) with the provided name exists (case insensitive)
// in the data.db.
func (app *BaseApp) HasTable(tableName string) bool {
	return app.hasTable(app.ConcurrentDB(), tableName)
}

// AuxHasTable checks if a table (or view) with the provided name exists (case insensitive)
// in the auxiliary.db.
func (app *BaseApp) AuxHasTable(tableName string) bool {
	return app.hasTable(app.AuxConcurrentDB(), tableName)
}

func (app *BaseApp) hasTable(db dbx.Builder, tableName string) bool {
	var exists int

	err := db.NewQuery(`
		SELECT 1
		FROM information_schema.tables
		WHERE table_schema = current_schema()
		  AND lower(table_name) = lower({:tableName})
		LIMIT 1
	`).Bind(dbx.Params{"tableName": tableName}).Row(&exists)

	return err == nil && exists > 0
}

// Vacuum executes VACUUM on the data database to reclaim unused disk space.
func (app *BaseApp) Vacuum() error {
	return app.vacuum(app.NonconcurrentDB())
}

// AuxVacuum executes VACUUM on the auxiliary database to reclaim unused disk space.
func (app *BaseApp) AuxVacuum() error {
	return app.vacuum(app.AuxNonconcurrentDB())
}

func (app *BaseApp) vacuum(db dbx.Builder) error {
	_, err := db.NewQuery("VACUUM").Execute()

	return err
}
