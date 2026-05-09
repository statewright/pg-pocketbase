package pgpb

import (
	"testing"
	"time"
)

func TestTempKV_SetAndGet(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_tempkv_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	kv := NewTempKV(db)
	if err := kv.createTable(); err != nil {
		t.Fatalf("createTable failed: %v", err)
	}

	// Set a value
	if err := kv.Set("key1", "value1", 1*time.Minute); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Get it back
	val, ok := kv.Get("key1")
	if !ok {
		t.Fatal("expected key1 to exist")
	}
	if val != "value1" {
		t.Fatalf("expected value1, got %q", val)
	}
}

func TestTempKV_GetNonexistent(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_tempkv_noexist_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	kv := NewTempKV(db)
	kv.createTable()

	_, ok := kv.Get("nonexistent")
	if ok {
		t.Fatal("nonexistent key should return false")
	}
}

func TestTempKV_Expiry(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_tempkv_expiry_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	kv := NewTempKV(db)
	kv.createTable()

	// Set with very short TTL — use PG's NOW() + interval
	// We can't use sub-second TTL with interval strings, so insert with
	// an already-expired timestamp directly
	db.Exec(`INSERT INTO "_pgpb_temp_kv" ("key", "value", "expires_at")
		VALUES ('expired_key', 'old_value', NOW() - INTERVAL '1 second')`)

	_, ok := kv.Get("expired_key")
	if ok {
		t.Fatal("expired key should not be returned")
	}
}

func TestTempKV_Delete(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_tempkv_delete_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	kv := NewTempKV(db)
	kv.createTable()

	kv.Set("to_delete", "val", 1*time.Minute)
	kv.Delete("to_delete")

	_, ok := kv.Get("to_delete")
	if ok {
		t.Fatal("deleted key should not be returned")
	}
}

func TestTempKV_Overwrite(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_tempkv_overwrite_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	kv := NewTempKV(db)
	kv.createTable()

	kv.Set("key", "value1", 1*time.Minute)
	kv.Set("key", "value2", 1*time.Minute)

	val, ok := kv.Get("key")
	if !ok {
		t.Fatal("key should exist")
	}
	if val != "value2" {
		t.Fatalf("expected value2, got %q", val)
	}
}

func TestTempKV_Cleanup(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_tempkv_cleanup_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	kv := NewTempKV(db)
	kv.createTable()

	// Insert some expired and some valid entries
	db.Exec(`INSERT INTO "_pgpb_temp_kv" ("key", "value", "expires_at")
		VALUES ('expired1', 'v', NOW() - INTERVAL '10 seconds')`)
	db.Exec(`INSERT INTO "_pgpb_temp_kv" ("key", "value", "expires_at")
		VALUES ('expired2', 'v', NOW() - INTERVAL '5 seconds')`)
	kv.Set("valid", "v", 1*time.Minute)

	kv.Cleanup()

	// Expired entries should be gone
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM "_pgpb_temp_kv"`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 remaining entry after cleanup, got %d", count)
	}

	// Valid entry should remain
	_, ok := kv.Get("valid")
	if !ok {
		t.Fatal("valid entry should survive cleanup")
	}
}

func TestTempKV_TableIdempotent(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_tempkv_idempotent_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	kv := NewTempKV(db)

	if err := kv.createTable(); err != nil {
		t.Fatalf("first createTable failed: %v", err)
	}
	if err := kv.createTable(); err != nil {
		t.Fatalf("second createTable should be idempotent: %v", err)
	}
}
