//go:build postgres

package core

import (
	"github.com/pocketbase/dbx"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// DefaultDBConnect opens a PostgreSQL database connection.
// When using the postgres build tag, the dbPath argument is treated
// as a PostgreSQL connection URL.
func DefaultDBConnect(dbPath string) (*dbx.DB, error) {
	db, err := dbx.Open("pgx", dbPath)
	if err != nil {
		return nil, err
	}

	return db, nil
}
