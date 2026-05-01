package pgpb

import (
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAdvisoryLockCron_SingleInstance(t *testing.T) {
	pgURL := getTestPostgresURL(t)

	dbName := "pgpb_test_cron_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	var ran atomic.Bool
	wrapped := WithAdvisoryLock(db, "test_job", func() {
		ran.Store(true)
	})

	wrapped()

	if !ran.Load() {
		t.Fatal("expected cron job to run on single instance")
	}
}

func TestAdvisoryLockCron_OnlyOneRuns(t *testing.T) {
	pgURL := getTestPostgresURL(t)

	dbName := "pgpb_test_cron_dedup_" + randomSuffix()
	db1 := openTestDB(t, pgURL, dbName)
	db2 := openTestDB(t, pgURL, dbName)
	defer func() {
		db1.Close()
		db2.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	var count atomic.Int32
	var wg sync.WaitGroup
	started := make(chan struct{})

	// Job on instance 1: acquire lock, signal, hold for a bit
	job1 := WithAdvisoryLock(db1, "dedup_job", func() {
		count.Add(1)
		close(started) // signal that lock is held
		time.Sleep(200 * time.Millisecond)
	})

	// Job on instance 2: should fail to acquire
	job2 := WithAdvisoryLock(db2, "dedup_job", func() {
		count.Add(1)
	})

	wg.Add(1)
	go func() {
		defer wg.Done()
		job1()
	}()

	// Wait for job1 to hold the lock
	<-started

	// Try job2 while job1 holds lock
	job2()

	wg.Wait()

	if got := count.Load(); got != 1 {
		t.Fatalf("expected exactly 1 execution, got %d", got)
	}
}

func TestAdvisoryLockCron_DifferentJobsRunConcurrently(t *testing.T) {
	pgURL := getTestPostgresURL(t)

	dbName := "pgpb_test_cron_diff_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	var count atomic.Int32

	jobA := WithAdvisoryLock(db, "job_a", func() {
		count.Add(1)
		time.Sleep(100 * time.Millisecond)
	})
	jobB := WithAdvisoryLock(db, "job_b", func() {
		count.Add(1)
	})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		jobA()
	}()
	go func() {
		defer wg.Done()
		jobB()
	}()
	wg.Wait()

	if got := count.Load(); got != 2 {
		t.Fatalf("expected 2 executions for different jobs, got %d", got)
	}
}

func TestHashJobID(t *testing.T) {
	// Same input must produce same output
	h1 := hashJobID("test_job")
	h2 := hashJobID("test_job")
	if h1 != h2 {
		t.Fatalf("expected deterministic hash, got %d and %d", h1, h2)
	}

	// Different inputs should produce different hashes (probabilistic)
	h3 := hashJobID("other_job")
	if h1 == h3 {
		t.Fatal("expected different hashes for different job IDs")
	}
}

// openTestDB creates/connects to a test database, returning *sql.DB
func openTestDB(t *testing.T, pgURL, dbName string) *sql.DB {
	t.Helper()

	// Ensure DB exists using our connect func
	connectFunc := PostgresDBConnect(pgURL)
	dbxDB, err := connectFunc(dbName)
	if err != nil {
		t.Fatalf("failed to create test db %q: %v", dbName, err)
	}
	dbxDB.Close()

	// Open raw sql.DB for cron tests
	u := mustParseConnURL(pgURL)
	u.Path = "/" + dbName
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatalf("failed to open %q: %v", dbName, err)
	}
	return db
}
