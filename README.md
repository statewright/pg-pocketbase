# pg-pocketbase

PostgreSQL backend for [PocketBase](https://pocketbase.io). Not a fork -- a surgical overlay using Go build tags and PB's `DBConnect` configuration hook.

PocketBase v0.37.3 | PostgreSQL 15+

## Why

PocketBase uses SQLite. That's fine until you need concurrent writes from multiple instances, or your infrastructure already runs PostgreSQL. This project swaps the storage engine without forking the entire PocketBase codebase.

- **Concurrent writes** via PostgreSQL MVCC (not serialized through a single connection)
- **Multi-instance** realtime sync via LISTEN/NOTIFY
- **Cron dedup** via `pg_try_advisory_lock`
- **Minimal upstream diff** -- build-tag pairs isolate all PG-specific code

## Quick Start

```go
package main

import (
    "log"
    "os"

    "github.com/statewright/pg-pocketbase/pgpb"
)

func main() {
    app := pgpb.NewWithPostgres(os.Getenv("POSTGRES_URL"))

    if err := app.Start(); err != nil {
        log.Fatal(err)
    }
}
```

```bash
# Build with PostgreSQL support (excludes SQLite driver)
go build -tags "postgres,no_default_driver" -o myapp .

# Run
POSTGRES_URL="postgres://user:pass@localhost:5432?sslmode=disable" ./myapp serve
```

Two databases are auto-created on first run: `pb_data` (main) and `pb_auxiliary` (logs).

## Multi-Instance

For horizontal scaling, enable the realtime bridge:

```go
app := pgpb.NewWithPostgres(os.Getenv("POSTGRES_URL"),
    pgpb.WithBridge(),
)
```

This starts LISTEN/NOTIFY channels that synchronize:
- SSE client subscriptions across instances
- Collection schema cache invalidation (including bulk imports)
- Settings changes
- Cron job deduplication (only one instance runs each job)

### Cache Invalidation

PostgreSQL triggers on `_collections` and `_settings` tables fire NOTIFY on any change, providing database-level cache invalidation that catches all write paths -- API requests, migrations, direct SQL. This is a safety net on top of the application-level hook broadcasts.

### Backup Coordination

Backup and restore operations are protected by PostgreSQL advisory locks across replicas. Only one replica can run a backup at a time, preventing concurrent backup corruption.

### Apple OAuth Name Handoff

Apple sends the user's name only on the OAuth redirect, which may hit a different replica than the subsequent auth callback. pg-pocketbase stores this temporary state in a PostgreSQL table (`_pgpb_temp_kv`) with a 1-minute TTL, so any replica can read it back.

### File Storage

Each replica has its own local `pb_data/storage/`. Without shared storage, files uploaded to one replica are invisible to others -- leading to broken file URLs and incomplete backups.

Configure S3 storage in PocketBase admin under Settings > S3 (and Backups > S3). [Trove](https://github.com/statewright/trove) is a lightweight, MIT-licensed S3-compatible object store that pairs well with pg-pocketbase -- it uses PostgreSQL for metadata and content-addressed filesystem storage, and can share the same PostgreSQL instance (tables are prefixed `trove_` to avoid conflicts).

```yaml
# docker-compose.yml (alongside pg-pocketbase)
services:
  trove:
    image: ghcr.io/statewright/trove:latest
    environment:
      TROVE_POSTGRES_URL: postgres://user:pass@postgres:5432/trove?sslmode=disable
      TROVE_DATA_DIR: /data
    volumes:
      - trove-data:/data
```

Then configure PocketBase: endpoint `http://trove:9000`, path-style URLs enabled, with the root access/secret keys printed to trove's stderr on first run.

### S3 Auto-Configuration

Instead of configuring S3 through the admin dashboard, set environment variables and pg-pocketbase applies them on startup:

```bash
PB_S3_ENABLED=true
PB_S3_ENDPOINT=http://trove:9000
PB_S3_BUCKET=pb-files
PB_S3_REGION=us-east-1
PB_S3_ACCESS_KEY=your-access-key
PB_S3_SECRET=your-secret-key
PB_S3_FORCE_PATH_STYLE=true
```

Backup storage uses the same pattern with the `PB_BACKUPS_S3_` prefix.

## Admin Auto-Elevation

pg-pocketbase can automatically promote allowed users to PocketBase superusers when they visit the admin dashboard (`/_/`). No shared admin passwords, no manual superuser management.

```bash
PGPB_ADMIN_EMAILS=admin@company.com,ops@company.com
```

When an authenticated user whose email is in the allowlist visits `/_/`:
1. A mapped superuser account is created (or its password rotated)
2. A short-lived auth token is generated (15 minutes, non-refreshable)
3. The user is redirected into the admin dashboard

The feature is completely inert when `PGPB_ADMIN_EMAILS` is unset. Superuser passwords are random and rotate on every elevation -- they never need to be known or stored.

## Configuration

```go
app := pgpb.NewWithPostgres(connString,
    pgpb.WithDataDBName("my_app_data"),     // default: "pb_data"
    pgpb.WithAuxDBName("my_app_logs"),      // default: "pb_auxiliary"
    pgpb.WithBridge(),                       // enable multi-instance sync
    pgpb.WithPocketBaseConfig(pocketbase.Config{
        DefaultDataDir: "/path/to/pb_data",
    }),
)
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `POSTGRES_URL` | *(required)* | PostgreSQL connection string |
| `PGPB_ADMIN_EMAILS` | *(empty)* | Comma-separated email allowlist for admin elevation |
| `PB_S3_ENABLED` | `false` | Enable S3 file storage |
| `PB_S3_ENDPOINT` | | S3 endpoint URL |
| `PB_S3_BUCKET` | | S3 bucket name |
| `PB_S3_REGION` | | S3 region |
| `PB_S3_ACCESS_KEY` | | S3 access key |
| `PB_S3_SECRET` | | S3 secret key |
| `PB_S3_FORCE_PATH_STYLE` | `false` | Use path-style S3 URLs |
| `PB_BACKUPS_S3_*` | | Same as above, for backup storage |
| `SENTRY_DSN` | | Sentry/GlitchTip error reporting DSN |

## Architecture

```
pg-pocketbase/
  pgpb/                          # companion module (zero upstream patches)
    pgpb.go                      # NewWithPostgres() entry point
    connect.go                   # connection routing, auto-create DB
    bootstrap.go                 # PG function shims (uuid_v7, hex, strftime, etc.)
    bridge.go                    # LISTEN/NOTIFY realtime bridge
    bridged_client.go            # cross-instance SSE client proxy
    cron.go                      # pg_try_advisory_lock cron dedup
    s3config.go                  # S3 auto-configuration from env vars
    admin_elevation.go           # admin dashboard auto-elevation
    backup_lock.go               # cross-replica backup advisory lock
    tempkv.go                    # PG-backed temporary key-value store

  pocketbase/                    # patched upstream (build-tag pairs)
    core/
      db_table_{sqlite,postgres}.go
      base_db_init_{sqlite,postgres}.go
      collection_query_{sqlite,postgres}.go
      collection_record_table_sync_{sqlite,postgres}.go
      ident_quote_{sqlite,postgres}.go
      ...
    tools/
      search/filter_{sqlite,postgres}.go
      search/sort_{sqlite,postgres}.go
      dbutils/json_{sqlite,postgres}.go
    apis/
      oauth2_apple_name_{sqlite,postgres}.go
    migrations/
      1640988000_init_{sqlite,postgres}.go
      1640988000_aux_init_{sqlite,postgres}.go
```

**Build tags:**
- `//go:build postgres` -- PostgreSQL implementations
- `//go:build !postgres` -- SQLite implementations (original upstream)
- `no_default_driver` -- excludes the SQLite driver from the binary

## PostgreSQL Function Shims

These are auto-created in each database before migrations run:

| Function | Purpose |
|----------|---------|
| `uuid_generate_v7()` | RFC 9562 UUIDv7 generation |
| `hex(bytea)` | SQLite `hex()` equivalent |
| `randomblob(int)` | Maps to `gen_random_bytes` |
| `json_valid(text)` | JSON validation |
| `JSON_EXTRACT(jsonb, text)` | SQLite `JSON_EXTRACT()` for log `data.*` filters |
| `json_query_or_null(jsonb, text)` | Safe JSON path extraction |
| `strftime(format, time_value)` | SQLite `strftime()` compatibility |
| `nocase` collation | Case-insensitive text comparison |

## Upstream Sync

This project tracks PocketBase upstream via the `UPSTREAM_VERSION` file.

```bash
make sync-upstream    # shows upstream changes to translation-surface files
make test-all         # runs both SQLite and PostgreSQL test suites
```

When PocketBase releases a new version:
1. Update `pocketbase/` to the new version
2. Check if any build-tag surface files changed upstream
3. Update corresponding `_postgres.go` files if needed
4. Run `make test-all`
5. Update `UPSTREAM_VERSION`

Most upstream changes won't touch translation-surface files. The build-tag architecture means untouched upstream files compile and work without modification.

## Development

```bash
# Start PostgreSQL
docker compose up -d

# Run PostgreSQL tests
PG_TEST_URL="postgres://pgpb:pgpb@localhost:5432?sslmode=disable" make test-pg

# Run SQLite tests (verify no regressions)
make test-sqlite

# Run both
make test-all
```

## License

MIT. See [LICENSE](LICENSE).

Portions derived from [PocketBase](https://github.com/pocketbase/pocketbase) by Gani Georgiev (MIT).
