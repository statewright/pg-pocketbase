package pgpb

import (
	"database/sql"
	"errors"
	"log/slog"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
)

const (
	backupLockID  = "pgpb_backup"
	restoreLockID = "pgpb_restore"
)

// BindBackupLock registers hooks that use PostgreSQL advisory locks to
// prevent concurrent backup/restore operations across replicas.
// Without this, each replica's local app.Store() mutex is independent,
// allowing two replicas to run backups simultaneously.
func BindBackupLock(app *pocketbase.PocketBase, db *sql.DB) {
	backupKey := hashJobID(backupLockID)
	restoreKey := hashJobID(restoreLockID)

	app.OnBackupCreate().Bind(&hook.Handler[*core.BackupEvent]{
		Id: "pgpb_backup_lock",
		Func: func(e *core.BackupEvent) error {
			if err := tryAcquire(db, backupKey); err != nil {
				return err
			}
			defer release(db, backupKey)
			return e.Next()
		},
		Priority: -999, // run first
	})

	app.OnBackupRestore().Bind(&hook.Handler[*core.BackupEvent]{
		Id: "pgpb_restore_lock",
		Func: func(e *core.BackupEvent) error {
			if err := tryAcquire(db, restoreKey); err != nil {
				return err
			}
			defer release(db, restoreKey)
			return e.Next()
		},
		Priority: -999,
	})
}

func tryAcquire(db *sql.DB, lockKey int64) error {
	var acquired bool
	if err := db.QueryRow("SELECT pg_try_advisory_lock($1)", lockKey).Scan(&acquired); err != nil {
		slog.Warn("pgpb: failed to acquire backup advisory lock",
			slog.String("error", err.Error()),
		)
		return errors.New("try again later - another backup/restore operation has already been started")
	}
	if !acquired {
		return errors.New("try again later - another backup/restore operation has already been started")
	}
	return nil
}

func release(db *sql.DB, lockKey int64) {
	if _, err := db.Exec("SELECT pg_advisory_unlock($1)", lockKey); err != nil {
		slog.Warn("pgpb: failed to release backup advisory lock",
			slog.String("error", err.Error()),
		)
	}
}
