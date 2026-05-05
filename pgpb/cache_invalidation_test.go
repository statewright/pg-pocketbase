package pgpb

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestBridge_CreateCacheInvalidationTriggers(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_cache_triggers_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	// Create the tables that triggers attach to (simulating PB migrations)
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS "_collections" (
		"id" TEXT PRIMARY KEY,
		"name" TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		t.Fatalf("failed to create _collections: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS "_settings" (
		"id" TEXT PRIMARY KEY,
		"value" JSONB NOT NULL DEFAULT '{}'
	)`)
	if err != nil {
		t.Fatalf("failed to create _settings: %v", err)
	}

	bridge := &RealtimeBridge{
		channelID: genChannelID(),
		db:        db,
	}

	if err := bridge.createCacheInvalidationTriggers(); err != nil {
		t.Fatalf("createCacheInvalidationTriggers failed: %v", err)
	}

	// Verify trigger function exists
	var fnExists bool
	err = db.QueryRow(`SELECT EXISTS (
		SELECT FROM pg_proc WHERE proname = '_pgpb_notify_cache_invalidate'
	)`).Scan(&fnExists)
	if err != nil || !fnExists {
		t.Fatal("trigger function _pgpb_notify_cache_invalidate not created")
	}

	// Verify triggers exist on both tables
	for _, table := range []string{"_collections", "_settings"} {
		var trigExists bool
		err = db.QueryRow(`SELECT EXISTS (
			SELECT FROM information_schema.triggers
			WHERE trigger_name = '_pgpb_cache_invalidate'
			AND event_object_table = $1
		)`, table).Scan(&trigExists)
		if err != nil || !trigExists {
			t.Fatalf("trigger not found on table %q", table)
		}
	}

	// Idempotent: calling again should not error
	if err := bridge.createCacheInvalidationTriggers(); err != nil {
		t.Fatalf("createCacheInvalidationTriggers not idempotent: %v", err)
	}
}

func TestBridge_CacheInvalidationTriggerFires(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_cache_trigger_fire_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	// Create target tables
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS "_collections" (
		"id" TEXT PRIMARY KEY,
		"name" TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		t.Fatalf("failed to create _collections: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS "_settings" (
		"id" TEXT PRIMARY KEY,
		"value" JSONB NOT NULL DEFAULT '{}'
	)`)
	if err != nil {
		t.Fatalf("failed to create _settings: %v", err)
	}

	bridge := &RealtimeBridge{
		channelID: genChannelID(),
		db:        db,
	}

	if err := bridge.createCacheInvalidationTriggers(); err != nil {
		t.Fatalf("createCacheInvalidationTriggers failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Listen on cache_invalidate channel
	u := mustParseConnURL(pgURL)
	u.Path = "/" + dbName
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("pgx connect failed: %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, "LISTEN "+cacheInvalidateChannel)
	if err != nil {
		t.Fatalf("LISTEN failed: %v", err)
	}

	// INSERT into _collections should fire the trigger
	_, err = db.ExecContext(ctx, `INSERT INTO "_collections" ("id", "name") VALUES ('test1', 'users')`)
	if err != nil {
		t.Fatalf("INSERT failed: %v", err)
	}

	notification, err := conn.WaitForNotification(ctx)
	if err != nil {
		t.Fatalf("WaitForNotification failed: %v", err)
	}

	if notification.Channel != cacheInvalidateChannel {
		t.Fatalf("expected channel %q, got %q", cacheInvalidateChannel, notification.Channel)
	}
	if notification.Payload != "_collections" {
		t.Fatalf("expected payload %q, got %q", "_collections", notification.Payload)
	}
}

func TestBridge_CacheInvalidationTriggerFiresOnUpdate(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_cache_trigger_upd_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS "_collections" (
		"id" TEXT PRIMARY KEY,
		"name" TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		t.Fatalf("failed to create _collections: %v", err)
	}

	bridge := &RealtimeBridge{channelID: genChannelID(), db: db}
	if err := bridge.createCacheInvalidationTriggers(); err != nil {
		t.Fatalf("createCacheInvalidationTriggers failed: %v", err)
	}

	// Pre-insert a row
	_, err = db.Exec(`INSERT INTO "_collections" ("id", "name") VALUES ('c1', 'posts')`)
	if err != nil {
		t.Fatalf("INSERT failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	u := mustParseConnURL(pgURL)
	u.Path = "/" + dbName
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("pgx connect failed: %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, "LISTEN "+cacheInvalidateChannel)
	if err != nil {
		t.Fatalf("LISTEN failed: %v", err)
	}

	// UPDATE should fire the trigger
	_, err = db.ExecContext(ctx, `UPDATE "_collections" SET "name" = 'articles' WHERE "id" = 'c1'`)
	if err != nil {
		t.Fatalf("UPDATE failed: %v", err)
	}

	notification, err := conn.WaitForNotification(ctx)
	if err != nil {
		t.Fatalf("WaitForNotification failed: %v", err)
	}

	if notification.Payload != "_collections" {
		t.Fatalf("expected payload %q, got %q", "_collections", notification.Payload)
	}
}

func TestBridge_CacheInvalidationTriggerFiresOnDelete(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_cache_trigger_del_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS "_settings" (
		"id" TEXT PRIMARY KEY,
		"value" JSONB NOT NULL DEFAULT '{}'
	)`)
	if err != nil {
		t.Fatalf("failed to create _settings: %v", err)
	}

	bridge := &RealtimeBridge{channelID: genChannelID(), db: db}
	if err := bridge.createCacheInvalidationTriggers(); err != nil {
		t.Fatalf("createCacheInvalidationTriggers failed: %v", err)
	}

	// Pre-insert
	_, err = db.Exec(`INSERT INTO "_settings" ("id", "value") VALUES ('s1', '{"smtp":true}')`)
	if err != nil {
		t.Fatalf("INSERT failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	u := mustParseConnURL(pgURL)
	u.Path = "/" + dbName
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("pgx connect failed: %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, "LISTEN "+cacheInvalidateChannel)
	if err != nil {
		t.Fatalf("LISTEN failed: %v", err)
	}

	// DELETE should fire the trigger
	_, err = db.ExecContext(ctx, `DELETE FROM "_settings" WHERE "id" = 's1'`)
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}

	notification, err := conn.WaitForNotification(ctx)
	if err != nil {
		t.Fatalf("WaitForNotification failed: %v", err)
	}

	if notification.Payload != "_settings" {
		t.Fatalf("expected payload %q, got %q", "_settings", notification.Payload)
	}
}

func TestBridge_ListenCacheInvalidation(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_cache_listen_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	// Create target tables and triggers
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS "_collections" (
		"id" TEXT PRIMARY KEY,
		"name" TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		t.Fatalf("failed to create _collections: %v", err)
	}

	connURL := mustParseConnURL(pgURL)
	connURL.Path = "/" + dbName
	bridge := &RealtimeBridge{
		channelID: genChannelID(),
		db:        db,
		connURL:   connURL.String(),
	}

	if err := bridge.createCacheInvalidationTriggers(); err != nil {
		t.Fatalf("createCacheInvalidationTriggers failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var received []string
	var mu sync.Mutex
	ready := make(chan struct{})

	go bridge.listenCacheInvalidation(ctx, func() {
		close(ready)
	}, func(tableName string) {
		mu.Lock()
		received = append(received, tableName)
		mu.Unlock()
	})

	<-ready

	// Trigger a change
	_, err = db.ExecContext(ctx, `INSERT INTO "_collections" ("id", "name") VALUES ('x1', 'test')`)
	if err != nil {
		t.Fatalf("INSERT failed: %v", err)
	}

	// Wait for receipt
	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		n := len(received)
		mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for cache invalidation notification")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if received[0] != "_collections" {
		t.Fatalf("expected table name %q, got %q", "_collections", received[0])
	}
}
