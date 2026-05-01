//go:build !postgres

package core

import (
	"log/slog"

	"github.com/pocketbase/dbx"
)

type viewInfo struct {
	Name string `db:"name"`
	SQL  string `db:"sql"`
}

// listAllViews returns all views from sqlite_master.
func listAllViews(txApp App) ([]viewInfo, error) {
	var views []viewInfo
	err := txApp.DB().Select("name", "sql").
		From("sqlite_master").
		AndWhere(dbx.NewExp("sql is not null")).
		AndWhere(dbx.HashExp{"type": "view"}).
		All(&views)
	return views, err
}

// postSyncOptimize runs PRAGMA optimize per SQLite recommendations.
func postSyncOptimize(app *BaseApp) {
	_, optimizeErr := app.NonconcurrentDB().NewQuery("PRAGMA optimize").Execute()
	if optimizeErr != nil {
		app.Logger().Warn("Failed to run PRAGMA optimize after record table sync", slog.String("error", optimizeErr.Error()))
	}
}
