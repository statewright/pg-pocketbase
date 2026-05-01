//go:build !postgres

package core

import "github.com/pocketbase/dbx"

// findIndexUsedByTable checks sqlite_master to see if an index name
// is already used by a table other than oldName or newName.
func findIndexUsedByTable(db dbx.Builder, oldName, newName, indexName string, usedTblName *string) error {
	return db.Select("tbl_name").
		From("sqlite_master").
		AndWhere(dbx.HashExp{"type": "index"}).
		AndWhere(dbx.NewExp("LOWER([[tbl_name]])!=LOWER({:oldName})", dbx.Params{"oldName": oldName})).
		AndWhere(dbx.NewExp("LOWER([[tbl_name]])!=LOWER({:newName})", dbx.Params{"newName": newName})).
		AndWhere(dbx.NewExp("LOWER([[name]])=LOWER({:indexName})", dbx.Params{"indexName": indexName})).
		Limit(1).
		Row(usedTblName)
}
