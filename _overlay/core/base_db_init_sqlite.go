//go:build !postgres

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

	concurrentDB, err := app.config.DBConnect(dbPath)
	if err != nil {
		return err
	}
	concurrentDB.DB().SetMaxOpenConns(app.config.DataMaxOpenConns)
	concurrentDB.DB().SetMaxIdleConns(app.config.DataMaxIdleConns)
	concurrentDB.DB().SetConnMaxIdleTime(3 * time.Minute)

	nonconcurrentDB, err := app.config.DBConnect(dbPath)
	if err != nil {
		return err
	}
	nonconcurrentDB.DB().SetMaxOpenConns(1)
	nonconcurrentDB.DB().SetMaxIdleConns(1)
	nonconcurrentDB.DB().SetConnMaxIdleTime(3 * time.Minute)

	if app.IsDev() {
		nonconcurrentDB.QueryLogFunc = func(ctx context.Context, t time.Duration, sql string, rows *sql.Rows, err error) {
			color.HiBlack("[%.2fms] %v\n", float64(t.Milliseconds()), normalizeSQLLog(sql))
		}
		nonconcurrentDB.ExecLogFunc = func(ctx context.Context, t time.Duration, sql string, result sql.Result, err error) {
			color.HiBlack("[%.2fms] %v\n", float64(t.Milliseconds()), normalizeSQLLog(sql))
		}
		concurrentDB.QueryLogFunc = nonconcurrentDB.QueryLogFunc
		concurrentDB.ExecLogFunc = nonconcurrentDB.ExecLogFunc
	}

	app.concurrentDB = concurrentDB
	app.nonconcurrentDB = nonconcurrentDB

	return nil
}

func (app *BaseApp) initAuxDB() error {
	// note: renamed to "auxiliary" because "aux" is a reserved Windows filename
	// (see https://github.com/pocketbase/pocketbase/issues/5607)
	dbPath := filepath.Join(app.DataDir(), "auxiliary.db")

	concurrentDB, err := app.config.DBConnect(dbPath)
	if err != nil {
		return err
	}
	concurrentDB.DB().SetMaxOpenConns(app.config.AuxMaxOpenConns)
	concurrentDB.DB().SetMaxIdleConns(app.config.AuxMaxIdleConns)
	concurrentDB.DB().SetConnMaxIdleTime(3 * time.Minute)

	nonconcurrentDB, err := app.config.DBConnect(dbPath)
	if err != nil {
		return err
	}
	nonconcurrentDB.DB().SetMaxOpenConns(1)
	nonconcurrentDB.DB().SetMaxIdleConns(1)
	nonconcurrentDB.DB().SetConnMaxIdleTime(3 * time.Minute)

	app.auxConcurrentDB = concurrentDB
	app.auxNonconcurrentDB = nonconcurrentDB

	return nil
}

func (app *BaseApp) registerDBOptimizeCron() {
	app.Cron().Add("__pbDBOptimize__", "0 0 * * *", func() {
		_, execErr := app.NonconcurrentDB().NewQuery("PRAGMA wal_checkpoint(TRUNCATE)").Execute()
		if execErr != nil {
			app.Logger().Warn("Failed to run periodic PRAGMA wal_checkpoint for the main DB", "error", execErr.Error())
		}

		_, execErr = app.AuxNonconcurrentDB().NewQuery("PRAGMA wal_checkpoint(TRUNCATE)").Execute()
		if execErr != nil {
			app.Logger().Warn("Failed to run periodic PRAGMA wal_checkpoint for the auxiliary DB", "error", execErr.Error())
		}

		_, execErr = app.NonconcurrentDB().NewQuery("PRAGMA optimize").Execute()
		if execErr != nil {
			app.Logger().Warn("Failed to run periodic PRAGMA optimize", "error", execErr.Error())
		}
	})
}
