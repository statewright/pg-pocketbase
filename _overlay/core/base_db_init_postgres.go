//go:build postgres

package core

import (
	"context"
	"database/sql"
	"path/filepath"
	"time"

	"github.com/fatih/color"
)

func (app *BaseApp) initDataDB() error {
	dbPath := filepath.Join(app.DataDir(), "data.db")

	// PostgreSQL: single connection pool for both concurrent and nonconcurrent.
	// PG handles concurrent writes via MVCC -- no need for a serialized write pool.
	concurrentDB, err := app.config.DBConnect(dbPath)
	if err != nil {
		return err
	}
	concurrentDB.DB().SetMaxOpenConns(app.config.DataMaxOpenConns)
	concurrentDB.DB().SetMaxIdleConns(app.config.DataMaxIdleConns)
	concurrentDB.DB().SetConnMaxIdleTime(3 * time.Minute)

	if app.IsDev() {
		concurrentDB.QueryLogFunc = func(ctx context.Context, t time.Duration, sql string, rows *sql.Rows, err error) {
			color.HiBlack("[%.2fms] %v\n", float64(t.Milliseconds()), normalizeSQLLog(sql))
		}
		concurrentDB.ExecLogFunc = func(ctx context.Context, t time.Duration, sql string, result sql.Result, err error) {
			color.HiBlack("[%.2fms] %v\n", float64(t.Milliseconds()), normalizeSQLLog(sql))
		}
	}

	// Both point to the same pool -- writes are not serialized.
	app.concurrentDB = concurrentDB
	app.nonconcurrentDB = concurrentDB

	return nil
}

func (app *BaseApp) initAuxDB() error {
	dbPath := filepath.Join(app.DataDir(), "auxiliary.db")

	concurrentDB, err := app.config.DBConnect(dbPath)
	if err != nil {
		return err
	}
	concurrentDB.DB().SetMaxOpenConns(app.config.AuxMaxOpenConns)
	concurrentDB.DB().SetMaxIdleConns(app.config.AuxMaxIdleConns)
	concurrentDB.DB().SetConnMaxIdleTime(3 * time.Minute)

	// Both point to the same pool.
	app.auxConcurrentDB = concurrentDB
	app.auxNonconcurrentDB = concurrentDB

	return nil
}

// registerDBOptimizeCron registers PostgreSQL-appropriate maintenance.
// SQLite PRAGMAs are replaced with VACUUM ANALYZE.
func (app *BaseApp) registerDBOptimizeCron() {
	app.Cron().Add("__pbDBOptimize__", "0 0 * * *", func() {
		_, execErr := app.NonconcurrentDB().NewQuery("VACUUM ANALYZE").Execute()
		if execErr != nil {
			app.Logger().Warn("Failed to run periodic VACUUM ANALYZE for the main DB", "error", execErr.Error())
		}

		_, execErr = app.AuxNonconcurrentDB().NewQuery("VACUUM ANALYZE").Execute()
		if execErr != nil {
			app.Logger().Warn("Failed to run periodic VACUUM ANALYZE for the auxiliary DB", "error", execErr.Error())
		}
	})
}
