package pgpb

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// getTestPostgresURL returns a PostgreSQL connection string for testing.
// Defaults to localhost if PG_TEST_URL is not set.
func getTestPostgresURL(t *testing.T) string {
	t.Helper()
	if u := os.Getenv("PG_TEST_URL"); u != "" {
		return u
	}
	return "postgres://localhost:5432?sslmode=disable"
}

func TestPostgresDBConnect_ValidURL(t *testing.T) {
	pgURL := getTestPostgresURL(t)

	connectFunc := PostgresDBConnect(pgURL)
	if connectFunc == nil {
		t.Fatal("PostgresDBConnect returned nil")
	}

	db, err := connectFunc("pgpb_test_valid_url")
	if err != nil {
		t.Fatalf("connectFunc failed: %v", err)
	}
	defer db.Close()

	// Verify it's a PostgreSQL connection (PgsqlBuilder)
	if db == nil {
		t.Fatal("returned db is nil")
	}

	// Verify we can ping
	if err := db.DB().Ping(); err != nil {
		t.Fatalf("ping failed: %v", err)
	}
}

func TestPostgresDBConnect_AutoCreateDB(t *testing.T) {
	pgURL := getTestPostgresURL(t)

	connectFunc := PostgresDBConnect(pgURL)

	dbName := "pgpb_test_autocreate_" + randomSuffix()

	db, err := connectFunc(dbName)
	if err != nil {
		t.Fatalf("connectFunc failed to auto-create database %q: %v", dbName, err)
	}
	defer func() {
		db.Close()
		// cleanup: drop the test database
		dropTestDB(t, pgURL, dbName)
	}()

	if err := db.DB().Ping(); err != nil {
		t.Fatalf("ping failed after auto-create: %v", err)
	}
}

func TestPostgresDBConnect_PoolSizing(t *testing.T) {
	pgURL := getTestPostgresURL(t)

	connectFunc := PostgresDBConnect(pgURL, WithMaxOpenConns(25), WithMaxIdleConns(5))

	dbName := "pgpb_test_pool_" + randomSuffix()
	db, err := connectFunc(dbName)
	if err != nil {
		t.Fatalf("connectFunc failed: %v", err)
	}
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	stats := db.DB().Stats()
	if stats.MaxOpenConnections != 25 {
		t.Errorf("expected MaxOpenConnections=25, got %d", stats.MaxOpenConnections)
	}
}

func TestPostgresDBConnect_InvalidScheme(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on invalid scheme, got none")
		}
	}()

	PostgresDBConnect("mysql://localhost:3306/test")
}

func TestPostgresDBConnect_ReusesExistingDB(t *testing.T) {
	pgURL := getTestPostgresURL(t)

	connectFunc := PostgresDBConnect(pgURL)
	dbName := "pgpb_test_reuse_" + randomSuffix()

	// First connection creates the DB
	db1, err := connectFunc(dbName)
	if err != nil {
		t.Fatalf("first connect failed: %v", err)
	}
	db1.Close()

	// Second connection reuses it
	db2, err := connectFunc(dbName)
	if err != nil {
		t.Fatalf("second connect failed: %v", err)
	}
	defer func() {
		db2.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	if err := db2.DB().Ping(); err != nil {
		t.Fatalf("ping failed on reuse: %v", err)
	}
}

// helpers

func randomSuffix() string {
	// simple timestamp-based suffix for test isolation
	return fmt.Sprintf("%d", time.Now().UnixNano()%100000)
}

func dropTestDB(t *testing.T, baseURL string, dbName string) {
	t.Helper()
	u, err := url.Parse(baseURL)
	if err != nil {
		t.Logf("warning: could not parse URL for cleanup: %v", err)
		return
	}
	u.Path = "/postgres"
	adminDB, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Logf("warning: could not connect for cleanup: %v", err)
		return
	}
	defer adminDB.Close()

	// Force disconnect other clients
	adminDB.Exec(fmt.Sprintf(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s' AND pid <> pg_backend_pid()`, dbName))
	_, err = adminDB.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS "%s"`, dbName))
	if err != nil {
		t.Logf("warning: failed to drop test database %q: %v", dbName, err)
	}
}
