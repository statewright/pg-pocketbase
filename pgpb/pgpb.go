// Package pgpb provides PostgreSQL support for PocketBase.
//
// Usage:
//
//	app := pgpb.NewWithPostgres("postgres://user:pass@localhost:5432?sslmode=disable")
//	if err := app.Start(); err != nil {
//		log.Fatal(err)
//	}
package pgpb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
)

const (
	DefaultDataMaxOpenConns = 70
	DefaultDataMaxIdleConns = 15
	DefaultAuxMaxOpenConns  = 20
	DefaultAuxMaxIdleConns  = 3
)

// Option configures the PostgreSQL PocketBase instance.
type Option func(*pgConfig)

type pgConfig struct {
	dataMaxOpenConns int
	dataMaxIdleConns int
	auxMaxOpenConns  int
	auxMaxIdleConns  int
	dataDBName       string
	auxDBName        string
	enableBridge     bool
	pbConfig         pocketbase.Config
}

func defaultPgConfig() pgConfig {
	return pgConfig{
		dataMaxOpenConns: DefaultDataMaxOpenConns,
		dataMaxIdleConns: DefaultDataMaxIdleConns,
		auxMaxOpenConns:  DefaultAuxMaxOpenConns,
		auxMaxIdleConns:  DefaultAuxMaxIdleConns,
		dataDBName:       "pb_data",
		auxDBName:        "pb_auxiliary",
	}
}

// WithDataDBName sets the PostgreSQL database name for the main data store.
func WithDataDBName(name string) Option {
	return func(c *pgConfig) { c.dataDBName = name }
}

// WithAuxDBName sets the PostgreSQL database name for the auxiliary store.
func WithAuxDBName(name string) Option {
	return func(c *pgConfig) { c.auxDBName = name }
}

// WithPocketBaseConfig allows passing a custom PocketBase config.
func WithPocketBaseConfig(cfg pocketbase.Config) Option {
	return func(c *pgConfig) { c.pbConfig = cfg }
}

// WithBridge enables the multi-instance realtime bridge.
// When enabled, LISTEN/NOTIFY is used to synchronize SSE subscriptions,
// collection schema changes, and settings across PocketBase instances.
func WithBridge() Option {
	return func(c *pgConfig) { c.enableBridge = true }
}

// NewWithPostgres creates a PocketBase instance backed by PostgreSQL.
//
// The connectionString should be a postgres:// URL pointing to the PostgreSQL server.
// Two databases will be created (or reused): one for data, one for auxiliary.
//
// The function bootstraps PostgreSQL-specific functions (uuid_generate_v7, etc.)
// and registers hooks to neutralize SQLite-specific maintenance crons.
func NewWithPostgres(connectionString string, opts ...Option) *pocketbase.PocketBase {
	cfg := defaultPgConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	pbCfg := cfg.pbConfig
	pbCfg.DBConnect = routeDBConnect(connectionString, cfg)
	pbCfg.DataMaxOpenConns = cfg.dataMaxOpenConns
	pbCfg.DataMaxIdleConns = cfg.dataMaxIdleConns
	pbCfg.AuxMaxOpenConns = cfg.auxMaxOpenConns
	pbCfg.AuxMaxIdleConns = cfg.auxMaxIdleConns

	app := pocketbase.NewWithConfig(pbCfg)

	// Bootstrap PG functions before migrations run
	app.OnBootstrap().Bind(&hook.Handler[*core.BootstrapEvent]{
		Id: "pgpb_bootstrap_functions",
		Func: func(e *core.BootstrapEvent) error {
			for _, dbName := range []string{cfg.dataDBName, cfg.auxDBName} {
				if err := BootstrapFunctions(connectionString, dbName); err != nil {
					return fmt.Errorf("pgpb: failed to bootstrap functions on %q: %w", dbName, err)
				}
			}
			return e.Next()
		},
		Priority: -999, // run before everything else
	})

	// Backup advisory lock (prevents concurrent backups across replicas)
	bindBackupLock(app, connectionString, cfg)

	// PG-backed temp KV store (for cross-replica state like Apple OAuth name handoff)
	bindTempKV(app, connectionString, cfg)

	if cfg.enableBridge {
		bindBridge(app, connectionString, cfg)
	}

	return app
}

// bindBackupLock opens a small connection pool for advisory lock operations
// and registers backup/restore hooks.
func bindBackupLock(app *pocketbase.PocketBase, connString string, cfg pgConfig) {
	u, err := url.Parse(connString)
	if err != nil {
		return
	}
	u.Path = "/" + cfg.dataDBName

	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Id: "pgpb_backup_lock_init",
		Func: func(e *core.ServeEvent) error {
			lockDB, err := sql.Open("pgx", u.String())
			if err != nil {
				slog.Warn("pgpb: failed to open backup lock db (non-fatal)",
					slog.String("error", err.Error()),
				)
				return e.Next()
			}
			lockDB.SetMaxOpenConns(2)
			lockDB.SetMaxIdleConns(1)

			BindBackupLock(app, lockDB)

			// Clean up on terminate
			app.OnTerminate().Bind(&hook.Handler[*core.TerminateEvent]{
				Id: "pgpb_backup_lock_close",
				Func: func(e *core.TerminateEvent) error {
					lockDB.Close()
					return e.Next()
				},
			})

			return e.Next()
		},
		Priority: 996, // before bridge (998)
	})
}

// bindTempKV initializes the PG-backed temp KV store for cross-replica state.
func bindTempKV(app *pocketbase.PocketBase, connString string, cfg pgConfig) {
	u, err := url.Parse(connString)
	if err != nil {
		return
	}
	u.Path = "/" + cfg.dataDBName

	kvDB, err := sql.Open("pgx", u.String())
	if err != nil {
		slog.Warn("pgpb: failed to open temp KV db (non-fatal)",
			slog.String("error", err.Error()),
		)
		return
	}
	kvDB.SetMaxOpenConns(3)
	kvDB.SetMaxIdleConns(1)

	BindTempKV(app, kvDB)

	app.OnTerminate().Bind(&hook.Handler[*core.TerminateEvent]{
		Id: "pgpb_tempkv_close",
		Func: func(e *core.TerminateEvent) error {
			kvDB.Close()
			return e.Next()
		},
	})
}

// bindBridge wires the multi-instance realtime bridge into PocketBase's lifecycle.
func bindBridge(app *pocketbase.PocketBase, connString string, cfg pgConfig) {
	// Build connection URL for the data database (used by LISTEN connections)
	u, err := url.Parse(connString)
	if err != nil {
		panic("pgpb: invalid connection string for bridge: " + err.Error())
	}
	u.Path = "/" + cfg.dataDBName
	dataConnURL := u.String()

	// Open a dedicated sql.DB for bridge operations (separate from PB's pool)
	bridgeDB, err := sql.Open("pgx", dataConnURL)
	if err != nil {
		panic("pgpb: failed to open bridge database: " + err.Error())
	}
	bridgeDB.SetMaxOpenConns(5)
	bridgeDB.SetMaxIdleConns(2)

	bridge := &RealtimeBridge{
		channelID: genChannelID(),
		db:        bridgeDB,
		connURL:   dataConnURL,
	}

	ctx, cancel := context.WithCancel(context.Background())

	// On serve: create tables, start background loops
	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Id: "pgpb_bridge_start",
		Func: func(e *core.ServeEvent) error {
			if err := bridge.createTables(); err != nil {
				return fmt.Errorf("pgpb bridge: %w", err)
			}

			// Install triggers on _collections and _settings for database-level
			// cache invalidation (catches migrations, direct SQL, all write paths).
			if err := bridge.createCacheInvalidationTriggers(); err != nil {
				slog.Warn("pgpb bridge: failed to create cache invalidation triggers (non-fatal)",
					slog.String("error", err.Error()),
				)
			}

			bridge.broker = app.SubscriptionsBroker()

			// Start heartbeat
			go bridge.heartbeatLoop(ctx)

			// Listen on shared channel for collection/settings/subscription broadcasts
			sharedReady := make(chan struct{})
			go bridge.listenSharedChannel(ctx, func() {
				close(sharedReady)
			}, func(msg bridgeMessage) {
				handleSharedMessage(app, bridge, msg)
			})

			// Listen on direct channel for client-targeted messages
			directReady := make(chan struct{})
			go bridge.listenDirectChannel(ctx, func() {
				close(directReady)
			}, func(dm directMessage) {
				handleDirectMessage(app, bridge, dm)
			})

			// Listen for trigger-based cache invalidation (database-level safety net)
			cacheReady := make(chan struct{})
			go bridge.listenCacheInvalidation(ctx, func() {
				close(cacheReady)
			}, func(tableName string) {
				switch tableName {
				case "_collections":
					if err := app.ReloadCachedCollections(); err != nil {
						slog.Warn("pgpb bridge: trigger-based collection reload failed",
							slog.String("error", err.Error()),
						)
					}
				case "_settings":
					if err := app.ReloadSettings(); err != nil {
						slog.Warn("pgpb bridge: trigger-based settings reload failed",
							slog.String("error", err.Error()),
						)
					}
				}
			})

			// Wait for listeners to be ready before proceeding
			<-sharedReady
			<-directReady
			<-cacheReady

			slog.Info("pgpb bridge: started",
				slog.String("channelID", bridge.channelID),
			)

			return e.Next()
		},
		Priority: 998, // just before cron start (999)
	})

	// On terminate: cancel background goroutines, close bridge DB
	app.OnTerminate().Bind(&hook.Handler[*core.TerminateEvent]{
		Id: "pgpb_bridge_stop",
		Func: func(e *core.TerminateEvent) error {
			cancel()
			bridgeDB.Close()
			return e.Next()
		},
	})

	// On realtime connect: wrap client with BridgedClient
	app.OnRealtimeConnectRequest().Bind(&hook.Handler[*core.RealtimeConnectRequestEvent]{
		Id: "pgpb_bridge_connect",
		Func: func(e *core.RealtimeConnectRequestEvent) error {
			bc := NewBridgedClient(e.Client, bridge)
			e.Client = bc
			return e.Next()
		},
		Priority: -999, // run first
	})

	// On subscription change: broadcast via bridge
	app.OnRealtimeSubscribeRequest().Bind(&hook.Handler[*core.RealtimeSubscribeRequestEvent]{
		Id: "pgpb_bridge_subscribe",
		Func: func(e *core.RealtimeSubscribeRequestEvent) error {
			err := e.Next()
			if err != nil {
				return err
			}

			// After subscriptions are applied, broadcast changes
			if bc, ok := e.Client.(*BridgedClient); ok {
				if bcErr := bc.BroadcastChanges(e.Request.Context()); bcErr != nil {
					slog.Warn("pgpb bridge: failed to broadcast subscription changes",
						slog.String("clientId", bc.Client.Id()),
						slog.String("error", bcErr.Error()),
					)
				}
			}

			return nil
		},
		Priority: 999, // run last
	})

	// On collection changes: broadcast via bridge
	collectionChangeHandler := &hook.Handler[*core.CollectionRequestEvent]{
		Id: "pgpb_bridge_collection_change",
		Func: func(e *core.CollectionRequestEvent) error {
			err := e.Next()
			if err != nil {
				return err
			}
			if bcErr := bridge.broadcastCollectionChanged(e.Request.Context()); bcErr != nil {
				slog.Warn("pgpb bridge: failed to broadcast collection change",
					slog.String("error", bcErr.Error()),
				)
			}
			return nil
		},
		Priority: 999,
	}
	app.OnCollectionCreateRequest().Bind(collectionChangeHandler)
	app.OnCollectionUpdateRequest().Bind(collectionChangeHandler)
	app.OnCollectionDeleteRequest().Bind(collectionChangeHandler)

	// On collection import: broadcast via bridge
	app.OnCollectionsImportRequest().Bind(&hook.Handler[*core.CollectionsImportRequestEvent]{
		Id: "pgpb_bridge_collection_import",
		Func: func(e *core.CollectionsImportRequestEvent) error {
			err := e.Next()
			if err != nil {
				return err
			}
			if bcErr := bridge.broadcastCollectionChanged(e.Request.Context()); bcErr != nil {
				slog.Warn("pgpb bridge: failed to broadcast collection import",
					slog.String("error", bcErr.Error()),
				)
			}
			return nil
		},
		Priority: 999,
	})

	// On settings change: broadcast via bridge
	app.OnSettingsUpdateRequest().Bind(&hook.Handler[*core.SettingsUpdateRequestEvent]{
		Id: "pgpb_bridge_settings_change",
		Func: func(e *core.SettingsUpdateRequestEvent) error {
			err := e.Next()
			if err != nil {
				return err
			}
			if bcErr := bridge.broadcastSettingsUpdated(e.Request.Context()); bcErr != nil {
				slog.Warn("pgpb bridge: failed to broadcast settings change",
					slog.String("error", bcErr.Error()),
				)
			}
			return nil
		},
		Priority: 999,
	})
}

// handleSharedMessage processes messages received on the shared bridge channel.
func handleSharedMessage(app *pocketbase.PocketBase, bridge *RealtimeBridge, msg bridgeMessage) {
	switch msg.Type {
	case msgTypeCollectionUpdated:
		if err := app.ReloadCachedCollections(); err != nil {
			slog.Warn("pgpb bridge: failed to reload collections",
				slog.String("error", err.Error()),
			)
		}

	case msgTypeSettingsUpdated:
		if err := app.ReloadSettings(); err != nil {
			slog.Warn("pgpb bridge: failed to reload settings",
				slog.String("error", err.Error()),
			)
		}

	case msgTypeSubscriptionUpsert:
		var rec clientRecord
		if err := json.Unmarshal(msg.Data, &rec); err != nil {
			slog.Warn("pgpb bridge: failed to parse subscription upsert",
				slog.String("error", err.Error()),
			)
			return
		}

		// Skip if sent by this instance
		if rec.UpdatedByChannel == bridge.channelID {
			return
		}

		// Check if client already exists in broker
		existing, err := app.SubscriptionsBroker().ClientById(rec.ClientID)
		if err == nil {
			// Update existing remote client
			if bc, ok := existing.(*BridgedClient); ok && bc.IsRemote() {
				bc.ReceiveChanges(rec)
			}
			return
		}

		// Register new remote client
		bc := NewRemoteBridgedClient(rec, bridge)
		app.SubscriptionsBroker().Register(bc)

	case msgTypeSubscriptionDelete:
		var data struct {
			ClientID string `json:"client_id"`
		}
		if err := json.Unmarshal(msg.Data, &data); err != nil {
			return
		}
		app.SubscriptionsBroker().Unregister(data.ClientID)

	case msgTypeChannelOffline:
		var data struct {
			ChannelID string `json:"channel_id"`
		}
		if err := json.Unmarshal(msg.Data, &data); err != nil {
			return
		}

		// Remove all remote clients from the dead channel
		for id, client := range app.SubscriptionsBroker().Clients() {
			if bc, ok := client.(*BridgedClient); ok && bc.IsRemote() && bc.record.ChannelID == data.ChannelID {
				app.SubscriptionsBroker().Unregister(id)
			}
		}
	}
}

// handleDirectMessage processes messages received on this instance's direct channel.
func handleDirectMessage(app *pocketbase.PocketBase, bridge *RealtimeBridge, dm directMessage) {
	client, err := app.SubscriptionsBroker().ClientById(dm.ClientID)
	if err != nil {
		slog.Debug("pgpb bridge: direct message for unknown client",
			slog.String("clientId", dm.ClientID),
		)
		return
	}

	// Only deliver to local clients (not remote BridgedClients)
	if bc, ok := client.(*BridgedClient); ok && bc.IsRemote() {
		slog.Warn("pgpb bridge: direct message arrived for remote client",
			slog.String("clientId", dm.ClientID),
		)
		return
	}

	client.Send(dm.Message)
}

// routeDBConnect returns a DBConnectFunc that routes PB's data.db/auxiliary.db
// paths to the correct PostgreSQL databases.
func routeDBConnect(connectionString string, cfg pgConfig) core.DBConnectFunc {
	dataConnect := PostgresDBConnect(connectionString,
		WithMaxOpenConns(cfg.dataMaxOpenConns),
		WithMaxIdleConns(cfg.dataMaxIdleConns),
	)
	auxConnect := PostgresDBConnect(connectionString,
		WithMaxOpenConns(cfg.auxMaxOpenConns),
		WithMaxIdleConns(cfg.auxMaxIdleConns),
	)

	return func(dbPath string) (*dbx.DB, error) {
		// PB calls with paths like "/path/to/pb_data/data.db" and "/path/to/pb_data/auxiliary.db"
		if strings.Contains(dbPath, "auxiliary") {
			return auxConnect(cfg.auxDBName)
		}
		return dataConnect(cfg.dataDBName)
	}
}
