package pgpb

import (
	"database/sql"
	"hash/fnv"
	"log/slog"
)

// WithAdvisoryLock wraps a cron function with pg_try_advisory_lock to ensure
// only one instance executes the job at a time across multiple PocketBase instances.
//
// The lock is acquired at the start and released when the function returns.
// If another instance already holds the lock, the function is silently skipped.
func WithAdvisoryLock(db *sql.DB, jobID string, fn func()) func() {
	lockKey := hashJobID(jobID)

	return func() {
		var acquired bool
		err := db.QueryRow("SELECT pg_try_advisory_lock($1)", lockKey).Scan(&acquired)
		if err != nil {
			slog.Warn("pgpb: failed to acquire advisory lock",
				slog.String("jobID", jobID),
				slog.String("error", err.Error()),
			)
			return
		}
		if !acquired {
			return
		}

		defer func() {
			_, _ = db.Exec("SELECT pg_advisory_unlock($1)", lockKey)
		}()

		fn()
	}
}

// hashJobID produces a deterministic int64 from a job ID string,
// suitable for use as a PostgreSQL advisory lock key.
func hashJobID(jobID string) int64 {
	h := fnv.New64a()
	h.Write([]byte(jobID))
	return int64(h.Sum64())
}
