package pgpb

import (
	"database/sql"
	"log/slog"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
)

// TempKV provides a PostgreSQL-backed temporary key-value store.
// Used for cross-replica state that needs to survive load-balancer routing
// (e.g., Apple OAuth name handoff between redirect and auth callback).
type TempKV struct {
	db *sql.DB
}

// NewTempKV creates a TempKV backed by the given database connection.
func NewTempKV(db *sql.DB) *TempKV {
	return &TempKV{db: db}
}

func (kv *TempKV) createTable() error {
	_, err := kv.db.Exec(`
		CREATE TABLE IF NOT EXISTS "_pgpb_temp_kv" (
			"key"        TEXT PRIMARY KEY,
			"value"      TEXT NOT NULL,
			"expires_at" TIMESTAMPTZ NOT NULL
		)
	`)
	return err
}

// Set stores a key-value pair with a TTL.
func (kv *TempKV) Set(key, value string, ttl time.Duration) error {
	_, err := kv.db.Exec(`
		INSERT INTO "_pgpb_temp_kv" ("key", "value", "expires_at")
		VALUES ($1, $2, NOW() + $3::interval)
		ON CONFLICT ("key") DO UPDATE SET
			"value" = EXCLUDED."value",
			"expires_at" = EXCLUDED."expires_at"
	`, key, value, ttl.String())
	return err
}

// Get retrieves a value by key. Returns ("", false) if not found or expired.
func (kv *TempKV) Get(key string) (string, bool) {
	var value string
	err := kv.db.QueryRow(`
		SELECT "value" FROM "_pgpb_temp_kv"
		WHERE "key" = $1 AND "expires_at" > NOW()
	`, key).Scan(&value)
	if err != nil {
		return "", false
	}
	return value, true
}

// Delete removes a key.
func (kv *TempKV) Delete(key string) {
	kv.db.Exec(`DELETE FROM "_pgpb_temp_kv" WHERE "key" = $1`, key)
}

// Cleanup removes all expired entries.
func (kv *TempKV) Cleanup() {
	kv.db.Exec(`DELETE FROM "_pgpb_temp_kv" WHERE "expires_at" <= NOW()`)
}

// BindTempKV initializes the PG-backed temp KV store and patches PocketBase's
// Apple OAuth name handoff to use it instead of the per-process app.Store().
func BindTempKV(app *pocketbase.PocketBase, db *sql.DB) {
	kv := NewTempKV(db)

	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Id: "pgpb_tempkv_init",
		Func: func(e *core.ServeEvent) error {
			if err := kv.createTable(); err != nil {
				slog.Warn("pgpb: failed to create temp KV table (non-fatal)",
					slog.String("error", err.Error()),
				)
			}
			return e.Next()
		},
		Priority: 995,
	})

	// Store the TempKV instance in app.Store() so the patched OAuth handlers can access it
	app.Store().Set("pgpb_tempkv", kv)
}
