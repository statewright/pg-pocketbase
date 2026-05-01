package pgpb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pocketbase/pocketbase/tools/subscriptions"
)

// BridgedClient wraps a subscriptions.Client with multi-instance awareness.
// For local clients, it broadcasts subscription changes and offline status
// via the bridge. For remote clients (created from NOTIFY messages), Send()
// routes messages through the bridge instead of the local channel.
type BridgedClient struct {
	subscriptions.Client
	bridge   *RealtimeBridge
	record   clientRecord
	isRemote bool
}

// NewBridgedClient wraps a local client with bridge awareness.
func NewBridgedClient(inner subscriptions.Client, bridge *RealtimeBridge) *BridgedClient {
	return &BridgedClient{
		Client: inner,
		bridge: bridge,
		record: clientRecord{
			ClientID:  inner.Id(),
			ChannelID: bridge.channelID,
		},
		isRemote: false,
	}
}

// NewRemoteBridgedClient creates a BridgedClient representing a client on another instance.
func NewRemoteBridgedClient(rec clientRecord, bridge *RealtimeBridge) *BridgedClient {
	inner := subscriptions.NewDefaultClient()
	// Override subscriptions from the remote record
	inner.Subscribe(rec.Subscriptions...)

	return &BridgedClient{
		Client:   inner,
		bridge:   bridge,
		record:   rec,
		isRemote: true,
	}
}

// IsRemote returns true if this client lives on another instance.
func (bc *BridgedClient) IsRemote() bool {
	return bc.isRemote
}

// Send routes messages to the correct destination.
// For local clients, it delegates to the inner client's channel.
// For remote clients, it sends via the bridge NOTIFY.
func (bc *BridgedClient) Send(m subscriptions.Message) {
	if bc.isRemote {
		ctx := context.Background()
		if err := bc.bridge.SendViaBridge(ctx, bc.record.ChannelID, bc.record.ClientID, m); err != nil {
			slog.Warn("pgpb bridge: failed to send via bridge",
				slog.String("clientId", bc.record.ClientID),
				slog.String("targetChannel", bc.record.ChannelID),
				slog.String("error", err.Error()),
			)
		}
		return
	}
	bc.Client.Send(m)
}

// Discard marks the client as discarded and broadcasts offline status.
func (bc *BridgedClient) Discard() {
	if !bc.isRemote {
		if err := bc.BroadcastGoOffline(context.Background()); err != nil {
			slog.Warn("pgpb bridge: failed to broadcast go offline on discard",
				slog.String("clientId", bc.Client.Id()),
				slog.String("error", err.Error()),
			)
		}
	}
	bc.Client.Discard()
}

// BroadcastChanges persists the current subscription state to the bridge
// database and notifies other instances.
func (bc *BridgedClient) BroadcastChanges(ctx context.Context) error {
	subs := bc.Client.Subscriptions()
	subKeys := make([]string, 0, len(subs))
	for k := range subs {
		subKeys = append(subKeys, k)
	}

	bc.record.Subscriptions = subKeys
	bc.record.UpdatedByChannel = bc.bridge.channelID

	// Upsert client record
	_, err := bc.bridge.db.ExecContext(ctx, `
		INSERT INTO "_realtime_clients" ("client_id", "channel_id", "subscriptions", "auth_collection_ref", "auth_record_ref", "updated_by_channel")
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT ("client_id") DO UPDATE SET
			"subscriptions" = EXCLUDED."subscriptions",
			"auth_collection_ref" = EXCLUDED."auth_collection_ref",
			"auth_record_ref" = EXCLUDED."auth_record_ref",
			"updated_by_channel" = EXCLUDED."updated_by_channel"
	`, bc.record.ClientID,
		bc.record.ChannelID,
		pgtype.FlatArray[string](bc.record.Subscriptions),
		bc.record.AuthCollectionRef,
		bc.record.AuthRecordRef,
		bc.record.UpdatedByChannel,
	)
	if err != nil {
		return fmt.Errorf("pgpb bridge: upsert client: %w", err)
	}

	// Notify via shared channel
	data, _ := json.Marshal(bc.record)
	return bc.bridge.notifyShared(ctx, msgTypeSubscriptionUpsert, data)
}

// BroadcastGoOffline removes this client from the bridge and notifies other instances.
func (bc *BridgedClient) BroadcastGoOffline(ctx context.Context) error {
	_, err := bc.bridge.db.ExecContext(ctx,
		`DELETE FROM "_realtime_clients" WHERE "client_id" = $1`,
		bc.record.ClientID,
	)
	if err != nil {
		return fmt.Errorf("pgpb bridge: delete client: %w", err)
	}

	data, _ := json.Marshal(map[string]string{
		"client_id": bc.record.ClientID,
	})
	return bc.bridge.notifyShared(ctx, msgTypeSubscriptionDelete, data)
}

// ReceiveChanges updates this remote client's subscription state from a bridge notification.
func (bc *BridgedClient) ReceiveChanges(rec clientRecord) {
	bc.Client.Unsubscribe()
	bc.Client.Subscribe(rec.Subscriptions...)
	bc.record = rec
}
