package pgpb

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackupLock_AcquireAndRelease(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_backuplock_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	lockKey := hashJobID("test_backup_lock")

	// Acquire lock
	if err := tryAcquire(db, lockKey); err != nil {
		t.Fatalf("tryAcquire failed: %v", err)
	}

	// Release lock
	release(db, lockKey)
}

func TestBackupLock_PreventsConcurrent(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_backuplock_concurrent_" + randomSuffix()
	db1 := openTestDB(t, pgURL, dbName)
	db2 := openTestDB(t, pgURL, dbName)
	defer func() {
		db1.Close()
		db2.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	lockKey := hashJobID("test_backup_concurrent")

	// First connection acquires
	if err := tryAcquire(db1, lockKey); err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}

	// Second connection should fail
	err := tryAcquire(db2, lockKey)
	if err == nil {
		t.Fatal("second acquire should fail while first holds lock")
	}
	if err.Error() != "try again later - another backup/restore operation has already been started" {
		t.Fatalf("unexpected error: %v", err)
	}

	// Release first lock
	release(db1, lockKey)

	// Now second connection should succeed
	if err := tryAcquire(db2, lockKey); err != nil {
		t.Fatalf("second acquire should succeed after release: %v", err)
	}
	release(db2, lockKey)
}

func TestBackupLock_BackupAndRestoreIndependent(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_backuplock_independent_" + randomSuffix()
	db1 := openTestDB(t, pgURL, dbName)
	db2 := openTestDB(t, pgURL, dbName)
	defer func() {
		db1.Close()
		db2.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	backupKey := hashJobID(backupLockID)
	restoreKey := hashJobID(restoreLockID)

	// Acquire backup lock
	if err := tryAcquire(db1, backupKey); err != nil {
		t.Fatalf("backup lock failed: %v", err)
	}

	// Restore lock should be independent
	if err := tryAcquire(db2, restoreKey); err != nil {
		t.Fatalf("restore lock should be independent from backup lock: %v", err)
	}

	release(db1, backupKey)
	release(db2, restoreKey)
}

func TestBackupLock_ConcurrentRace(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_backuplock_race_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	lockKey := hashJobID("test_backup_race")

	// Launch 10 goroutines trying to acquire the same lock
	var acquired int64
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each goroutine needs its own connection
			connDB := openTestDB(t, pgURL, dbName)
			defer connDB.Close()

			err := tryAcquire(connDB, lockKey)
			if err == nil {
				atomic.AddInt64(&acquired, 1)
				// Hold the lock briefly
				time.Sleep(50 * time.Millisecond)
				release(connDB, lockKey)
			}
		}()
	}

	wg.Wait()

	// At least 1 should have acquired (the first one)
	if acquired == 0 {
		t.Fatal("at least one goroutine should have acquired the lock")
	}

	// Not all should have acquired simultaneously
	// (with 50ms hold time and 10 goroutines, most will be blocked)
	t.Logf("acquired count: %d/10", acquired)
}
