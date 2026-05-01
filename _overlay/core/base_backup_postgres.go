//go:build postgres

package core

// preBackupCheckpoint is a no-op on PostgreSQL (no WAL checkpoint needed).
func preBackupCheckpoint(txApp App) {}
