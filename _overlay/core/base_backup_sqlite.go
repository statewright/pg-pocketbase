//go:build !postgres

package core

// preBackupCheckpoint runs PRAGMA wal_checkpoint to truncate WAL files before backup.
func preBackupCheckpoint(txApp App) {
	txApp.DB().NewQuery("PRAGMA wal_checkpoint(TRUNCATE)").Execute()
	txApp.AuxDB().NewQuery("PRAGMA wal_checkpoint(TRUNCATE)").Execute()
}
