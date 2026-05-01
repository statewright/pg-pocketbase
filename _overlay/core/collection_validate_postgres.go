//go:build postgres

package core

import "github.com/pocketbase/dbx"

// findIndexUsedByTable checks pg_indexes to see if an index name
// is already used by a table other than oldName or newName.
func findIndexUsedByTable(db dbx.Builder, oldName, newName, indexName string, usedTblName *string) error {
	return db.Select("tablename").
		From("pg_indexes").
		AndWhere(dbx.NewExp("LOWER([[tablename]])!=LOWER({:oldName})", dbx.Params{"oldName": oldName})).
		AndWhere(dbx.NewExp("LOWER([[tablename]])!=LOWER({:newName})", dbx.Params{"newName": newName})).
		AndWhere(dbx.NewExp("LOWER([[indexname]])=LOWER({:indexName})", dbx.Params{"indexName": indexName})).
		Limit(1).
		Row(usedTblName)
}
