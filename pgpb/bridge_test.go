package pgpb

import (
	"context"
	"encoding/json"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pocketbase/pocketbase/tools/subscriptions"
)

func TestBridge_ChannelIDUnique(t *testing.T) {
	id1 := genChannelID()
	id2 := genChannelID()

	if id1 == id2 {
		t.Fatalf("channel IDs should be unique, got %q twice", id1)
	}

	// Must be valid PG identifier (lowercase, no special chars except underscore)
	for _, id := range []string{id1, id2} {
		if id == "" {
			t.Fatal("channel ID must not be empty")
		}
		for _, c := range id {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
				t.Fatalf("channel ID %q contains invalid char %q", id, string(c))
			}
		}
	}
}

func TestBridge_CreateTables(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_bridge_tables_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	bridge := &RealtimeBridge{
		channelID: genChannelID(),
		db:        db,
	}

	if err := bridge.createTables(); err != nil {
		t.Fatalf("createTables failed: %v", err)
	}

	// Verify tables exist
	var exists bool
	err := db.QueryRow(`SELECT EXISTS (
		SELECT FROM information_schema.tables WHERE table_name = '_realtime_channels'
	)`).Scan(&exists)
	if err != nil || !exists {
		t.Fatal("_realtime_channels table not created")
	}

	err = db.QueryRow(`SELECT EXISTS (
		SELECT FROM information_schema.tables WHERE table_name = '_realtime_clients'
	)`).Scan(&exists)
	if err != nil || !exists {
		t.Fatal("_realtime_clients table not created")
	}

	// Idempotent: calling again should not error
	if err := bridge.createTables(); err != nil {
		t.Fatalf("createTables not idempotent: %v", err)
	}
}

func TestBridge_HeartbeatRegistersChannel(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_bridge_heartbeat_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	bridge := &RealtimeBridge{
		channelID: genChannelID(),
		db:        db,
	}
	if err := bridge.createTables(); err != nil {
		t.Fatalf("createTables failed: %v", err)
	}

	if err := bridge.heartbeat(); err != nil {
		t.Fatalf("heartbeat failed: %v", err)
	}

	var channelID string
	err := db.QueryRow(`SELECT "channel_id" FROM "_realtime_channels" WHERE "channel_id" = $1`,
		bridge.channelID).Scan(&channelID)
	if err != nil {
		t.Fatalf("channel not registered: %v", err)
	}
	if channelID != bridge.channelID {
		t.Fatalf("expected channel %q, got %q", bridge.channelID, channelID)
	}
}

func TestBridge_HeartbeatRemovesStaleChannels(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_bridge_stale_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	bridge := &RealtimeBridge{
		channelID: genChannelID(),
		db:        db,
	}
	if err := bridge.createTables(); err != nil {
		t.Fatalf("createTables failed: %v", err)
	}

	// Insert a stale channel (expired 10 seconds ago)
	staleID := "stale_channel_test"
	_, err := db.Exec(`INSERT INTO "_realtime_channels" ("channel_id", "valid_until")
		VALUES ($1, NOW() - INTERVAL '10 seconds')`, staleID)
	if err != nil {
		t.Fatalf("failed to insert stale channel: %v", err)
	}

	// Heartbeat should clean it up
	if err := bridge.heartbeat(); err != nil {
		t.Fatalf("heartbeat failed: %v", err)
	}

	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM "_realtime_channels" WHERE "channel_id" = $1`,
		staleID).Scan(&count)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("stale channel should have been removed, count=%d", count)
	}
}

func TestBridge_NotifyAndListen(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_bridge_notify_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	channel := "test_notify_channel"
	payload := "hello_world"

	// Set up listener on dedicated connection
	u := mustParseConnURL(pgURL)
	u.Path = "/" + dbName
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("pgx connect failed: %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, "LISTEN "+channel)
	if err != nil {
		t.Fatalf("LISTEN failed: %v", err)
	}

	// Send notification
	_, err = db.ExecContext(ctx, "SELECT pg_notify($1, $2)", channel, payload)
	if err != nil {
		t.Fatalf("pg_notify failed: %v", err)
	}

	// Receive
	notification, err := conn.WaitForNotification(ctx)
	if err != nil {
		t.Fatalf("WaitForNotification failed: %v", err)
	}

	if notification.Channel != channel {
		t.Fatalf("expected channel %q, got %q", channel, notification.Channel)
	}
	if notification.Payload != payload {
		t.Fatalf("expected payload %q, got %q", payload, notification.Payload)
	}
}

func TestBridge_BroadcastCollectionChange(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_bridge_colchange_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bridge := &RealtimeBridge{
		channelID: genChannelID(),
		db:        db,
	}

	// Listen on shared channel
	u := mustParseConnURL(pgURL)
	u.Path = "/" + dbName
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("pgx connect failed: %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, "LISTEN "+sharedBridgeChannel)
	if err != nil {
		t.Fatalf("LISTEN failed: %v", err)
	}

	// Broadcast collection change
	if err := bridge.broadcastCollectionChanged(ctx); err != nil {
		t.Fatalf("broadcastCollectionChanged failed: %v", err)
	}

	notification, err := conn.WaitForNotification(ctx)
	if err != nil {
		t.Fatalf("WaitForNotification failed: %v", err)
	}

	var msg bridgeMessage
	if err := json.Unmarshal([]byte(notification.Payload), &msg); err != nil {
		t.Fatalf("failed to parse notification: %v", err)
	}

	if msg.Type != msgTypeCollectionUpdated {
		t.Fatalf("expected message type %q, got %q", msgTypeCollectionUpdated, msg.Type)
	}
	if msg.SenderID != bridge.channelID {
		t.Fatalf("expected sender %q, got %q", bridge.channelID, msg.SenderID)
	}
}

func TestBridge_BroadcastSettingsChange(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_bridge_settings_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bridge := &RealtimeBridge{
		channelID: genChannelID(),
		db:        db,
	}

	u := mustParseConnURL(pgURL)
	u.Path = "/" + dbName
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("pgx connect failed: %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, "LISTEN "+sharedBridgeChannel)
	if err != nil {
		t.Fatalf("LISTEN failed: %v", err)
	}

	if err := bridge.broadcastSettingsUpdated(ctx); err != nil {
		t.Fatalf("broadcastSettingsUpdated failed: %v", err)
	}

	notification, err := conn.WaitForNotification(ctx)
	if err != nil {
		t.Fatalf("WaitForNotification failed: %v", err)
	}

	var msg bridgeMessage
	if err := json.Unmarshal([]byte(notification.Payload), &msg); err != nil {
		t.Fatalf("failed to parse notification: %v", err)
	}

	if msg.Type != msgTypeSettingsUpdated {
		t.Fatalf("expected message type %q, got %q", msgTypeSettingsUpdated, msg.Type)
	}
}

func TestBridge_ClientSubscriptionBroadcast(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_bridge_clientsub_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bridge := &RealtimeBridge{
		channelID: genChannelID(),
		db:        db,
	}
	if err := bridge.createTables(); err != nil {
		t.Fatalf("createTables failed: %v", err)
	}

	// Create a local client
	innerClient := subscriptions.NewDefaultClient()
	innerClient.Subscribe("collection_a/*", "collection_b/abc123")

	bc := NewBridgedClient(innerClient, bridge)

	// Listen for broadcast
	u := mustParseConnURL(pgURL)
	u.Path = "/" + dbName
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("pgx connect failed: %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, "LISTEN "+sharedBridgeChannel)
	if err != nil {
		t.Fatalf("LISTEN failed: %v", err)
	}

	// Broadcast subscription state
	if err := bc.BroadcastChanges(ctx); err != nil {
		t.Fatalf("BroadcastChanges failed: %v", err)
	}

	// Verify notification
	notification, err := conn.WaitForNotification(ctx)
	if err != nil {
		t.Fatalf("WaitForNotification failed: %v", err)
	}

	var msg bridgeMessage
	if err := json.Unmarshal([]byte(notification.Payload), &msg); err != nil {
		t.Fatalf("failed to parse notification: %v", err)
	}
	if msg.Type != msgTypeSubscriptionUpsert {
		t.Fatalf("expected type %q, got %q", msgTypeSubscriptionUpsert, msg.Type)
	}

	// Verify client record persisted in DB
	var clientID string
	err = db.QueryRow(`SELECT "client_id" FROM "_realtime_clients" WHERE "client_id" = $1`,
		innerClient.Id()).Scan(&clientID)
	if err != nil {
		t.Fatalf("client not persisted: %v", err)
	}
}

func TestBridge_ClientOfflineBroadcast(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_bridge_offline_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bridge := &RealtimeBridge{
		channelID: genChannelID(),
		db:        db,
	}
	if err := bridge.createTables(); err != nil {
		t.Fatalf("createTables failed: %v", err)
	}

	innerClient := subscriptions.NewDefaultClient()
	bc := NewBridgedClient(innerClient, bridge)

	// Persist the client first
	if err := bc.BroadcastChanges(ctx); err != nil {
		t.Fatalf("BroadcastChanges failed: %v", err)
	}

	// Listen for offline broadcast
	u := mustParseConnURL(pgURL)
	u.Path = "/" + dbName
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("pgx connect failed: %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, "LISTEN "+sharedBridgeChannel)
	if err != nil {
		t.Fatalf("LISTEN failed: %v", err)
	}

	// Go offline
	if err := bc.BroadcastGoOffline(ctx); err != nil {
		t.Fatalf("BroadcastGoOffline failed: %v", err)
	}

	notification, err := conn.WaitForNotification(ctx)
	if err != nil {
		t.Fatalf("WaitForNotification failed: %v", err)
	}

	var msg bridgeMessage
	if err := json.Unmarshal([]byte(notification.Payload), &msg); err != nil {
		t.Fatalf("failed to parse notification: %v", err)
	}
	if msg.Type != msgTypeSubscriptionDelete {
		t.Fatalf("expected type %q, got %q", msgTypeSubscriptionDelete, msg.Type)
	}

	// Verify client removed from DB
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM "_realtime_clients" WHERE "client_id" = $1`,
		innerClient.Id()).Scan(&count)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if count != 0 {
		t.Fatal("client should have been removed from DB")
	}
}

func TestBridge_SendViaBridge(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_bridge_send_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	targetChannel := "c_target_inst_12345"

	bridge := &RealtimeBridge{
		channelID: genChannelID(),
		db:        db,
	}

	// Listen on target channel
	u := mustParseConnURL(pgURL)
	u.Path = "/" + dbName
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("pgx connect failed: %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, "LISTEN "+targetChannel)
	if err != nil {
		t.Fatalf("LISTEN failed: %v", err)
	}

	msg := subscriptions.Message{
		Name: "test_event",
		Data: []byte(`{"test":"data"}`),
	}

	if err := bridge.SendViaBridge(ctx, targetChannel, "client123", msg); err != nil {
		t.Fatalf("SendViaBridge failed: %v", err)
	}

	notification, err := conn.WaitForNotification(ctx)
	if err != nil {
		t.Fatalf("WaitForNotification failed: %v", err)
	}

	if notification.Channel != targetChannel {
		t.Fatalf("expected channel %q, got %q", targetChannel, notification.Channel)
	}

	// Parse direct message
	var dm directMessage
	if err := json.Unmarshal([]byte(notification.Payload), &dm); err != nil {
		t.Fatalf("failed to parse: %v", err)
	}
	if dm.ClientID != "client123" {
		t.Fatalf("expected client %q, got %q", "client123", dm.ClientID)
	}
	if dm.Message.Name != "test_event" {
		t.Fatalf("expected message name %q, got %q", "test_event", dm.Message.Name)
	}
}

func TestBridge_ListenLoop(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_bridge_listen_" + randomSuffix()
	db := openTestDB(t, pgURL, dbName)
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	connURL := mustParseConnURL(pgURL)
	connURL.Path = "/" + dbName
	bridge := &RealtimeBridge{
		channelID: genChannelID(),
		db:        db,
		connURL:   connURL.String(),
	}
	if err := bridge.createTables(); err != nil {
		t.Fatalf("createTables failed: %v", err)
	}

	// Start listening on shared channel
	var received []bridgeMessage
	var mu sync.Mutex
	ready := make(chan struct{})

	go bridge.listenSharedChannel(ctx, func() {
		close(ready)
	}, func(msg bridgeMessage) {
		mu.Lock()
		received = append(received, msg)
		mu.Unlock()
	})

	<-ready // wait for LISTEN to be established

	// Send a notification from another connection
	_, err := db.ExecContext(ctx, "SELECT pg_notify($1, $2)",
		sharedBridgeChannel,
		`{"type":"collection_updated","sender_id":"other_instance"}`)
	if err != nil {
		t.Fatalf("pg_notify failed: %v", err)
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
			t.Fatal("timed out waiting for notification")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if received[0].Type != msgTypeCollectionUpdated {
		t.Fatalf("expected type %q, got %q", msgTypeCollectionUpdated, received[0].Type)
	}
}

// mustParseConnURL parses a postgres URL, panicking on error.
// Exported for use in tests.
func mustParseConnURL(pgURL string) *url.URL {
	u, err := url.Parse(pgURL)
	if err != nil {
		panic("invalid test postgres URL: " + err.Error())
	}
	return u
}
