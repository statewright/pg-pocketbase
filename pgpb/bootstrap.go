package pgpb

import (
	"database/sql"
	"fmt"
	"net/url"
)

// PostgresFunctionShims contains all the CREATE FUNCTION statements needed
// to provide SQLite-equivalent functions in PostgreSQL.
// These must run before any PocketBase migration.
var PostgresFunctionShims = []string{
	// pgcrypto extension for gen_random_bytes
	`CREATE EXTENSION IF NOT EXISTS "pgcrypto"`,

	// Case-insensitive collation (SQLite NOCASE equivalent)
	`DO $$ BEGIN
		CREATE COLLATION IF NOT EXISTS "nocase" (
			provider = icu,
			locale = 'und-u-ks-level2',
			deterministic = false
		);
	EXCEPTION WHEN duplicate_object THEN NULL;
	END $$`,

	// hex(bytea) -> text (SQLite built-in)
	`CREATE OR REPLACE FUNCTION hex(data bytea)
	RETURNS text
	LANGUAGE SQL
	IMMUTABLE
	AS $fn$
		SELECT encode(data, 'hex')
	$fn$`,

	// randomblob(integer) -> bytea (SQLite built-in)
	`CREATE OR REPLACE FUNCTION randomblob(length integer)
	RETURNS bytea
	LANGUAGE SQL
	VOLATILE
	AS $fn$
		SELECT gen_random_bytes(length)
	$fn$`,

	// uuid_generate_v7() -> uuid (RFC 9562 UUIDv7)
	`CREATE OR REPLACE FUNCTION uuid_generate_v7()
	RETURNS uuid
	AS $fn$
	BEGIN
		RETURN encode(
			set_bit(
				set_bit(
					overlay(uuid_send(gen_random_uuid())
						placing substring(int8send(floor(extract(epoch from clock_timestamp()) * 1000)::bigint) from 3)
						from 1 for 6
					),
					52, 1
				),
				53, 1
			),
			'hex')::uuid;
	END
	$fn$
	LANGUAGE plpgsql
	VOLATILE`,

	// json_valid(text) -> boolean (SQLite built-in)
	`CREATE OR REPLACE FUNCTION json_valid(text) RETURNS boolean AS $fn$
	BEGIN
		PERFORM $1::jsonb;
		RETURN TRUE;
	EXCEPTION WHEN others THEN
		RETURN FALSE;
	END;
	$fn$ LANGUAGE plpgsql IMMUTABLE`,

	// JSON_EXTRACT(jsonb, text) -> text (SQLite JSON_EXTRACT compatibility)
	// PocketBase's SimpleFieldResolver hardcodes JSON_EXTRACT() for data.* filters
	// (e.g., log queries with data.status = 200). This shim translates to PostgreSQL's
	// jsonb path extraction, returning text to match SQLite's behavior.
	`CREATE OR REPLACE FUNCTION "JSON_EXTRACT"(p_input jsonb, p_path text) RETURNS text AS $fn$
	DECLARE
		pg_path text[];
		result jsonb;
	BEGIN
		-- Convert SQLite JSON path '$.foo.bar' to PostgreSQL path array {'foo','bar'}
		pg_path := string_to_array(ltrim(p_path, '$.'), '.');
		result := p_input #> pg_path;
		-- Return unquoted text for scalars (matching SQLite JSON_EXTRACT behavior)
		IF jsonb_typeof(result) = 'string' THEN
			RETURN result #>> '{}';
		END IF;
		RETURN result::text;
	EXCEPTION WHEN others THEN
		RETURN NULL;
	END;
	$fn$ LANGUAGE plpgsql IMMUTABLE`,

	// JSON_EXTRACT(text, text) -> text (overload for text columns)
	`CREATE OR REPLACE FUNCTION "JSON_EXTRACT"(p_input text, p_path text) RETURNS text AS $fn$
	BEGIN
		RETURN "JSON_EXTRACT"(p_input::jsonb, p_path);
	EXCEPTION WHEN others THEN
		RETURN NULL;
	END;
	$fn$ LANGUAGE plpgsql IMMUTABLE`,

	// json_query_or_null(jsonb, text) -> jsonb (safe JSON path extraction)
	`CREATE OR REPLACE FUNCTION json_query_or_null(p_input jsonb, p_query text) RETURNS jsonb AS $fn$
		SELECT JSON_QUERY(p_input, p_query)
	$fn$ LANGUAGE sql IMMUTABLE`,

	// json_query_or_null(anyelement, text) -> jsonb (polymorphic overload)
	`CREATE OR REPLACE FUNCTION json_query_or_null(p_input anyelement, p_query text) RETURNS jsonb AS $fn$
	BEGIN
		RETURN JSON_QUERY(p_input::text::jsonb, p_query);
	EXCEPTION WHEN others THEN
		RETURN NULL;
	END;
	$fn$ LANGUAGE plpgsql STABLE`,

	// strftime(format, time_value) -> text (SQLite strftime compatibility)
	// Translates common SQLite format specifiers to PostgreSQL to_char patterns.
	`CREATE OR REPLACE FUNCTION strftime(format text, time_value text DEFAULT NULL)
	RETURNS text AS $fn$
	DECLARE
		pg_format text;
		ts timestamptz;
	BEGIN
		IF time_value IS NULL THEN
			ts := NOW();
		ELSE
			ts := time_value::timestamptz;
		END IF;

		pg_format := format;
		pg_format := replace(pg_format, '%Y', 'YYYY');
		pg_format := replace(pg_format, '%m', 'MM');
		pg_format := replace(pg_format, '%d', 'DD');
		pg_format := replace(pg_format, '%H', 'HH24');
		pg_format := replace(pg_format, '%M', 'MI');
		pg_format := replace(pg_format, '%f', 'MS');
		pg_format := replace(pg_format, '%S', 'SS');
		pg_format := replace(pg_format, '%j', 'DDD');
		pg_format := replace(pg_format, '%W', 'IW');
		pg_format := replace(pg_format, '%w', 'D');

		IF pg_format = '%s' THEN
			RETURN floor(extract(epoch FROM ts))::text;
		END IF;

		RETURN to_char(ts, pg_format);
	EXCEPTION WHEN others THEN
		RETURN NULL;
	END;
	$fn$ LANGUAGE plpgsql STABLE`,
}

// BootstrapFunctions creates all PostgreSQL function shims required by PocketBase.
// This must be called on each database before running migrations.
// If the database does not exist, it will be auto-created.
func BootstrapFunctions(connString string, dbName string) error {
	u, err := url.Parse(connString)
	if err != nil {
		return fmt.Errorf("pgpb: invalid connection string: %w", err)
	}
	u.Path = "/" + dbName

	db, err := sql.Open("pgx", u.String())
	if err != nil {
		return fmt.Errorf("pgpb: failed to connect to %q: %w", dbName, err)
	}
	defer db.Close()

	// Auto-create database if it doesn't exist
	if pingErr := db.Ping(); pingErr != nil {
		db.Close()
		if reDBNotExist.MatchString(pingErr.Error()) {
			if createErr := createDatabase(u, dbName); createErr != nil {
				return fmt.Errorf("pgpb: failed to auto-create database %q: %w", dbName, createErr)
			}
			db, err = sql.Open("pgx", u.String())
			if err != nil {
				return fmt.Errorf("pgpb: failed to reopen %q after creation: %w", dbName, err)
			}
			defer db.Close()
		} else {
			return fmt.Errorf("pgpb: failed to connect to %q: %w", dbName, pingErr)
		}
	}

	for i, stmt := range PostgresFunctionShims {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("pgpb: failed to execute shim %d: %w", i, err)
		}
	}

	return nil
}
