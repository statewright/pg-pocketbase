package pgpb

import (
	"database/sql"
	"net/url"
	"testing"
)

func TestBootstrapFunctions(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_bootstrap_" + randomSuffix()

	// Create the test database first
	connectFunc := PostgresDBConnect(pgURL)
	db, err := connectFunc(dbName)
	if err != nil {
		t.Fatalf("failed to create test db: %v", err)
	}
	db.Close()

	defer dropTestDB(t, pgURL, dbName)

	// Bootstrap the functions
	if err := BootstrapFunctions(pgURL, dbName); err != nil {
		t.Fatalf("BootstrapFunctions failed: %v", err)
	}

	// Connect and verify each function exists
	u, _ := url.Parse(pgURL)
	u.Path = "/" + dbName
	testDB, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatalf("failed to connect for verification: %v", err)
	}
	defer testDB.Close()

	t.Run("hex function", func(t *testing.T) {
		var result string
		err := testDB.QueryRow(`SELECT hex(E'\\xDEADBEEF'::bytea)`).Scan(&result)
		if err != nil {
			t.Fatalf("hex() failed: %v", err)
		}
		if result != "deadbeef" {
			t.Errorf("expected deadbeef, got %q", result)
		}
	})

	t.Run("randomblob function", func(t *testing.T) {
		var length int
		err := testDB.QueryRow(`SELECT octet_length(randomblob(16))`).Scan(&length)
		if err != nil {
			t.Fatalf("randomblob() failed: %v", err)
		}
		if length != 16 {
			t.Errorf("expected 16 bytes, got %d", length)
		}
	})

	t.Run("uuid_generate_v7 function", func(t *testing.T) {
		var uuid string
		err := testDB.QueryRow(`SELECT uuid_generate_v7()::text`).Scan(&uuid)
		if err != nil {
			t.Fatalf("uuid_generate_v7() failed: %v", err)
		}
		if len(uuid) != 36 {
			t.Errorf("expected 36-char UUID, got %q (len %d)", uuid, len(uuid))
		}
		// UUIDv7 has version nibble '7' at position 14
		if uuid[14] != '7' {
			t.Errorf("expected version 7 at position 14, got %c in %q", uuid[14], uuid)
		}
	})

	t.Run("json_valid function", func(t *testing.T) {
		var valid bool
		err := testDB.QueryRow(`SELECT json_valid('{"key": "value"}')`).Scan(&valid)
		if err != nil {
			t.Fatalf("json_valid() failed: %v", err)
		}
		if !valid {
			t.Error("expected valid JSON to return true")
		}

		err = testDB.QueryRow(`SELECT json_valid('not json')`).Scan(&valid)
		if err != nil {
			t.Fatalf("json_valid() with invalid input failed: %v", err)
		}
		if valid {
			t.Error("expected invalid JSON to return false")
		}
	})

	t.Run("json_query_or_null function", func(t *testing.T) {
		var result sql.NullString
		err := testDB.QueryRow(`SELECT json_query_or_null('{"a": [1,2,3]}'::jsonb, '$.a')::text`).Scan(&result)
		if err != nil {
			t.Fatalf("json_query_or_null() failed: %v", err)
		}
		if !result.Valid || result.String != "[1, 2, 3]" {
			t.Errorf("expected [1, 2, 3], got %v", result)
		}
	})

	t.Run("nocase collation", func(t *testing.T) {
		var matches bool
		err := testDB.QueryRow(`SELECT 'Hello' = 'hello' COLLATE "nocase"`).Scan(&matches)
		if err != nil {
			t.Fatalf("nocase collation test failed: %v", err)
		}
		if !matches {
			t.Error("expected case-insensitive match with nocase collation")
		}
	})

	t.Run("strftime function", func(t *testing.T) {
		// Format with explicit time value
		var result string
		err := testDB.QueryRow(`SELECT strftime('%Y-%m-%d', '2026-04-30 12:30:45.123Z')`).Scan(&result)
		if err != nil {
			t.Fatalf("strftime() with time value failed: %v", err)
		}
		if result != "2026-04-30" {
			t.Errorf("expected '2026-04-30', got %q", result)
		}

		// Hourly truncation (same pattern used by LogsStats)
		err = testDB.QueryRow(`SELECT strftime('%Y-%m-%d %H:00:00', '2026-04-30 12:30:45.123Z')`).Scan(&result)
		if err != nil {
			t.Fatalf("strftime() hourly truncation failed: %v", err)
		}
		if result != "2026-04-30 12:00:00" {
			t.Errorf("expected '2026-04-30 12:00:00', got %q", result)
		}

		// Format with no time value (defaults to NOW)
		err = testDB.QueryRow(`SELECT strftime('%Y')`).Scan(&result)
		if err != nil {
			t.Fatalf("strftime() with default NOW failed: %v", err)
		}
		if result != "2026" {
			t.Errorf("expected current year '2026', got %q", result)
		}
	})

	t.Run("idempotent re-run", func(t *testing.T) {
		// Running bootstrap again should not error
		if err := BootstrapFunctions(pgURL, dbName); err != nil {
			t.Fatalf("second BootstrapFunctions run failed: %v", err)
		}
	})
}
