package pgpb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pocketbase/pocketbase/tools/subscriptions"
)

const (
	sharedBridgeChannel      = "pb_bridge_shared"
	cacheInvalidateChannel   = "cache_invalidate"

	heartbeatInterval = 30 * time.Second
	heartbeatTTL      = 40 * time.Second

	msgTypeSubscriptionUpsert   = "subscription_upsert"
	msgTypeSubscriptionDelete   = "subscription_delete"
	msgTypeChannelOffline       = "channel_offline"
	msgTypeCollectionUpdated    = "collection_updated"
	msgTypeSettingsUpdated      = "settings_updated"

	// Maximum NOTIFY payload is 8000 bytes.
	maxNotifyPayload = 7500
)

var reValidPGIdent = regexp.MustCompile(`^[a-z0-9_]+$`)

// bridgeMessage is the JSON structure sent via NOTIFY on the shared channel.
type bridgeMessage struct {
	Type     string          `json:"type"`
	SenderID string          `json:"sender_id"`
	Data     json.RawMessage `json:"data,omitempty"`
}

// directMessage is sent via NOTIFY on instance-specific channels.
type directMessage struct {
	ClientID string              `json:"client_id"`
	Message  subscriptions.Message `json:"message"`
}

// clientRecord is the DB representation of a remote client's subscription state.
type clientRecord struct {
	ClientID          string   `json:"client_id"`
	ChannelID         string   `json:"channel_id"`
	Subscriptions     []string `json:"subscriptions"`
	AuthCollectionRef string   `json:"auth_collection_ref"`
	AuthRecordRef     string   `json:"auth_record_ref"`
	UpdatedByChannel  string   `json:"updated_by_channel"`
}

// RealtimeBridge coordinates multi-instance state synchronization
// using PostgreSQL LISTEN/NOTIFY.
type RealtimeBridge struct {
	channelID string
	db        *sql.DB
	connURL   string // for dedicated pgx connections (LISTEN loops)
	broker    *subscriptions.Broker
}

// ChannelID returns this instance's unique channel identifier.
func (b *RealtimeBridge) ChannelID() string {
	return b.channelID
}

// createTables creates the bridge coordination tables if they don't exist.
func (b *RealtimeBridge) createTables() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS "_realtime_channels" (
			"channel_id" TEXT PRIMARY KEY,
			"valid_until" TIMESTAMPTZ NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS "_realtime_clients" (
			"client_id" TEXT PRIMARY KEY,
			"channel_id" TEXT NOT NULL,
			"subscriptions" TEXT[] NOT NULL DEFAULT '{}',
			"auth_collection_ref" TEXT NOT NULL DEFAULT '',
			"auth_record_ref" TEXT NOT NULL DEFAULT '',
			"updated_by_channel" TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS "_realtime_clients_channel_id_idx"
			ON "_realtime_clients" ("channel_id")`,
	}

	for _, stmt := range stmts {
		if _, err := b.db.Exec(stmt); err != nil {
			return fmt.Errorf("pgpb bridge: createTables: %w", err)
		}
	}

	return nil
}

// heartbeat upserts this instance's channel entry and removes stale channels.
// Returns the list of channel IDs that were removed as stale.
func (b *RealtimeBridge) heartbeat() error {
	// Upsert our channel with fresh TTL
	_, err := b.db.Exec(`
		INSERT INTO "_realtime_channels" ("channel_id", "valid_until")
		VALUES ($1, NOW() + $2::interval)
		ON CONFLICT ("channel_id") DO UPDATE
		SET "valid_until" = EXCLUDED."valid_until"
	`, b.channelID, heartbeatTTL.String())
	if err != nil {
		return fmt.Errorf("pgpb bridge: heartbeat upsert: %w", err)
	}

	// Delete stale channels and their clients in one shot
	rows, err := b.db.Query(`
		WITH stale AS (
			DELETE FROM "_realtime_channels"
			WHERE "valid_until" < NOW()
			RETURNING "channel_id"
		)
		DELETE FROM "_realtime_clients"
		WHERE "channel_id" IN (SELECT "channel_id" FROM stale)
		RETURNING "channel_id"
	`)
	if err != nil {
		return fmt.Errorf("pgpb bridge: heartbeat cleanup: %w", err)
	}
	defer rows.Close()

	// Notify about offline channels
	offlineChannels := map[string]bool{}
	for rows.Next() {
		var chID string
		if err := rows.Scan(&chID); err != nil {
			continue
		}
		offlineChannels[chID] = true
	}

	for chID := range offlineChannels {
		b.notifyShared(context.Background(), msgTypeChannelOffline, mustJSON(map[string]string{
			"channel_id": chID,
		}))
	}

	return nil
}

// heartbeatLoop runs the heartbeat at regular intervals with jitter.
func (b *RealtimeBridge) heartbeatLoop(ctx context.Context) {
	for {
		if err := b.heartbeat(); err != nil {
			slog.Warn("pgpb bridge: heartbeat error", slog.String("error", err.Error()))
		}

		jitter := time.Duration(rand.Int64N(int64(5 * time.Second)))
		select {
		case <-ctx.Done():
			return
		case <-time.After(heartbeatInterval + jitter):
		}
	}
}

// broadcastCollectionChanged notifies all instances that collections have changed.
func (b *RealtimeBridge) broadcastCollectionChanged(ctx context.Context) error {
	return b.notifyShared(ctx, msgTypeCollectionUpdated, nil)
}

// broadcastSettingsUpdated notifies all instances that settings have changed.
func (b *RealtimeBridge) broadcastSettingsUpdated(ctx context.Context) error {
	return b.notifyShared(ctx, msgTypeSettingsUpdated, nil)
}

// SendViaBridge sends a message to a specific client on a remote instance
// via the instance's direct NOTIFY channel.
func (b *RealtimeBridge) SendViaBridge(ctx context.Context, targetChannel string, clientID string, msg subscriptions.Message) error {
	dm := directMessage{
		ClientID: clientID,
		Message:  msg,
	}

	payload, err := json.Marshal(dm)
	if err != nil {
		return fmt.Errorf("pgpb bridge: marshal direct message: %w", err)
	}

	if len(payload) > maxNotifyPayload {
		return fmt.Errorf("pgpb bridge: direct message payload too large (%d bytes, max %d)", len(payload), maxNotifyPayload)
	}

	_, err = b.db.ExecContext(ctx, "SELECT pg_notify($1, $2)", targetChannel, string(payload))
	if err != nil {
		return fmt.Errorf("pgpb bridge: SendViaBridge notify: %w", err)
	}

	return nil
}

// notifyShared sends a typed message on the shared bridge channel.
func (b *RealtimeBridge) notifyShared(ctx context.Context, msgType string, data json.RawMessage) error {
	msg := bridgeMessage{
		Type:     msgType,
		SenderID: b.channelID,
		Data:     data,
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("pgpb bridge: marshal: %w", err)
	}

	if len(payload) > maxNotifyPayload {
		return fmt.Errorf("pgpb bridge: shared message too large (%d bytes, max %d)", len(payload), maxNotifyPayload)
	}

	_, err = b.db.ExecContext(ctx, "SELECT pg_notify($1, $2)", sharedBridgeChannel, string(payload))
	if err != nil {
		return fmt.Errorf("pgpb bridge: notify shared: %w", err)
	}

	return nil
}

// listenSharedChannel listens on the shared bridge channel and dispatches
// parsed messages to the handler. Calls onReady once LISTEN is established.
// Reconnects on error. Exits when ctx is cancelled.
func (b *RealtimeBridge) listenSharedChannel(ctx context.Context, onReady func(), handler func(bridgeMessage)) {
	b.listenLoop(ctx, sharedBridgeChannel, onReady, func(n *pgconn.Notification) {
		var msg bridgeMessage
		if err := json.Unmarshal([]byte(n.Payload), &msg); err != nil {
			slog.Warn("pgpb bridge: failed to parse shared notification",
				slog.String("error", err.Error()),
				slog.String("payload", n.Payload),
			)
			return
		}

		// Skip our own messages
		if msg.SenderID == b.channelID {
			return
		}

		handler(msg)
	})
}

// listenDirectChannel listens on this instance's direct channel for
// messages targeted at local clients. Calls onReady once LISTEN is established.
func (b *RealtimeBridge) listenDirectChannel(ctx context.Context, onReady func(), handler func(directMessage)) {
	b.listenLoop(ctx, b.channelID, onReady, func(n *pgconn.Notification) {
		var dm directMessage
		if err := json.Unmarshal([]byte(n.Payload), &dm); err != nil {
			slog.Warn("pgpb bridge: failed to parse direct notification",
				slog.String("error", err.Error()),
				slog.String("payload", n.Payload),
			)
			return
		}

		handler(dm)
	})
}

// listenLoop opens a dedicated pgx connection, LISTENs on the channel,
// and dispatches notifications. Reconnects with backoff on error.
func (b *RealtimeBridge) listenLoop(ctx context.Context, channel string, onReady func(), handler func(*pgconn.Notification)) {
	readyCalled := false

	for {
		err := b.listenOnce(ctx, channel, func() {
			if !readyCalled {
				readyCalled = true
				onReady()
			}
		}, handler)

		if ctx.Err() != nil {
			return
		}

		slog.Warn("pgpb bridge: listen connection lost, reconnecting",
			slog.String("channel", channel),
			slog.String("error", err.Error()),
		)

		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// listenOnce opens one connection, LISTENs, and blocks dispatching notifications.
func (b *RealtimeBridge) listenOnce(ctx context.Context, channel string, onReady func(), handler func(*pgconn.Notification)) error {
	conn, err := pgx.Connect(ctx, b.connURL)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, "LISTEN "+channel)
	if err != nil {
		return fmt.Errorf("LISTEN: %w", err)
	}

	onReady()

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("WaitForNotification: %w", err)
		}
		handler(notification)
	}
}

// genChannelID generates a unique channel identifier for this instance.
// Format: c_{hostname}_{random} -- all lowercase, valid PG identifier.
func genChannelID() string {
	hostname, _ := os.Hostname()
	// Normalize hostname to valid PG identifier chars
	normalized := make([]byte, 0, len(hostname))
	for _, c := range []byte(hostname) {
		switch {
		case c >= 'a' && c <= 'z':
			normalized = append(normalized, c)
		case c >= 'A' && c <= 'Z':
			normalized = append(normalized, c+32) // lowercase
		case c >= '0' && c <= '9':
			normalized = append(normalized, c)
		default:
			normalized = append(normalized, '_')
		}
	}
	if len(normalized) > 20 {
		normalized = normalized[:20]
	}

	suffix := fmt.Sprintf("%05d", rand.Int64N(100000))
	return "c_" + string(normalized) + "_" + suffix
}

// createCacheInvalidationTriggers installs PostgreSQL triggers on _collections
// and _settings that fire NOTIFY on any change. This provides database-level
// cache invalidation that catches all write paths (API, migrations, direct SQL)
// regardless of whether the application-level hook fires.
func (b *RealtimeBridge) createCacheInvalidationTriggers() error {
	stmts := []string{
		`CREATE OR REPLACE FUNCTION _pgpb_notify_cache_invalidate() RETURNS trigger AS $$
		BEGIN
			PERFORM pg_notify('cache_invalidate', TG_TABLE_NAME);
			RETURN NULL;
		END;
		$$ LANGUAGE plpgsql`,

		`DROP TRIGGER IF EXISTS _pgpb_cache_invalidate ON "_collections"`,
		`CREATE TRIGGER _pgpb_cache_invalidate
			AFTER INSERT OR UPDATE OR DELETE ON "_collections"
			FOR EACH STATEMENT EXECUTE FUNCTION _pgpb_notify_cache_invalidate()`,

		`DROP TRIGGER IF EXISTS _pgpb_cache_invalidate ON "_settings"`,
		`CREATE TRIGGER _pgpb_cache_invalidate
			AFTER INSERT OR UPDATE OR DELETE ON "_settings"
			FOR EACH STATEMENT EXECUTE FUNCTION _pgpb_notify_cache_invalidate()`,
	}

	for _, stmt := range stmts {
		if _, err := b.db.Exec(stmt); err != nil {
			return fmt.Errorf("pgpb bridge: createCacheInvalidationTriggers: %w", err)
		}
	}

	return nil
}

// listenCacheInvalidation listens on the cache_invalidate channel for
// trigger-fired notifications. Calls onReady once LISTEN is established.
func (b *RealtimeBridge) listenCacheInvalidation(ctx context.Context, onReady func(), handler func(tableName string)) {
	b.listenLoop(ctx, cacheInvalidateChannel, onReady, func(n *pgconn.Notification) {
		handler(n.Payload)
	})
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("pgpb bridge: mustJSON: " + err.Error())
	}
	return b
}
