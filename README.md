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
- Collection schema cache invalidation
- Settings changes
- Cron job deduplication (only one instance runs each job)

File storage must use S3 or equivalent shared storage -- this is a documented PocketBase requirement for multi-instance deployments regardless of database.

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

  pocketbase/                    # patched upstream (build-tag pairs)
    core/
      db_table_{sqlite,postgres}.go          # schema introspection
      base_db_init_{sqlite,postgres}.go      # pool setup, maintenance crons
      collection_query_{sqlite,postgres}.go  # ordering (rowid vs id)
      collection_validate_{sqlite,postgres}.go
      collection_record_table_sync_{sqlite,postgres}.go
      ident_quote_{sqlite,postgres}.go       # ` vs "
      ...
    tools/
      search/filter_{sqlite,postgres}.go     # type coercion, JSON operators
      search/sort_{sqlite,postgres}.go       # @rowid mapping
      dbutils/json_{sqlite,postgres}.go      # json_extract vs jsonb operators
      dbutils/index_quote_{sqlite,postgres}.go
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
