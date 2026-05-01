//go:build postgres

package core

type viewInfo struct {
	Name string `db:"name"`
	SQL  string `db:"sql"`
}

// listAllViews returns all views from pg_views.
func listAllViews(txApp App) ([]viewInfo, error) {
	var views []viewInfo
	err := txApp.DB().NewQuery(`
		SELECT viewname AS name, definition AS sql
		FROM pg_views
		WHERE schemaname = current_schema()
	`).All(&views)
	return views, err
}

// postSyncOptimize is a no-op on PostgreSQL (PRAGMA is SQLite-specific).
func postSyncOptimize(app *BaseApp) {
	// PostgreSQL uses autovacuum; no manual optimization needed here.
}
