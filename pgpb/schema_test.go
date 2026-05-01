package pgpb

import (
	"database/sql"
	"net/url"
	"testing"
)

// TestSchemaIntrospection verifies that the PostgreSQL schema introspection
// queries from db_table_postgres.go work correctly against a real PG instance.
// These tests validate the information_schema queries directly.
func TestSchemaIntrospection(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_schema_" + randomSuffix()

	// Create and bootstrap the test database
	connectFunc := PostgresDBConnect(pgURL)
	db, err := connectFunc(dbName)
	if err != nil {
		t.Fatalf("failed to create test db: %v", err)
	}
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	if err := BootstrapFunctions(pgURL, dbName); err != nil {
		t.Fatalf("bootstrap failed: %v", err)
	}

	// Create a test table
	_, err = db.NewQuery(`
		CREATE TABLE IF NOT EXISTS test_table (
			id TEXT PRIMARY KEY DEFAULT uuid_generate_v7()::text NOT NULL,
			name TEXT DEFAULT '' NOT NULL,
			age INTEGER DEFAULT 0 NOT NULL,
			email TEXT DEFAULT '' NOT NULL,
			data JSONB DEFAULT '{}' NOT NULL,
			active BOOLEAN DEFAULT FALSE NOT NULL
		)
	`).Execute()
	if err != nil {
		t.Fatalf("failed to create test table: %v", err)
	}

	// Create an index
	_, err = db.NewQuery(`
		CREATE UNIQUE INDEX idx_test_table_email ON test_table (email)
	`).Execute()
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	// Test information_schema column query (mirrors TableColumns)
	t.Run("TableColumns query", func(t *testing.T) {
		var columns []string
		err := db.NewQuery(`
			SELECT column_name
			FROM information_schema.columns
			WHERE table_name = {:tableName}
			  AND table_schema = current_schema()
		`).Bind(map[string]any{"tableName": "test_table"}).Column(&columns)
		if err != nil {
			t.Fatalf("query failed: %v", err)
		}
		if len(columns) != 6 {
			t.Errorf("expected 6 columns, got %d: %v", len(columns), columns)
		}
	})

	// Test information_schema table info query (mirrors TableInfo)
	t.Run("TableInfo query", func(t *testing.T) {
		type infoRow struct {
			PK           int            `db:"pk"`
			Index        int            `db:"cid"`
			Name         string         `db:"name"`
			Type         string         `db:"type"`
			NotNull      bool           `db:"notnull"`
			DefaultValue sql.NullString `db:"dflt_value"`
		}
		var info []infoRow
		err := db.NewQuery(`
			SELECT
				ordinal_position - 1 AS cid,
				c.column_name AS name,
				data_type AS type,
				CASE WHEN is_nullable = 'NO' THEN 1 ELSE 0 END AS notnull,
				column_default AS dflt_value,
				CASE WHEN pk.constraint_type = 'PRIMARY KEY' THEN 1 ELSE 0 END AS pk
			FROM information_schema.columns c
			LEFT JOIN (
				SELECT ccu.column_name, tc.constraint_type
				FROM information_schema.table_constraints tc
				JOIN information_schema.constraint_column_usage ccu
					ON tc.constraint_name = ccu.constraint_name
				WHERE tc.constraint_type = 'PRIMARY KEY'
					AND tc.table_name = {:tableName}
					AND tc.table_schema = current_schema()
			) pk ON c.column_name = pk.column_name
			WHERE c.table_name = {:tableName}
				AND c.table_schema = current_schema()
			ORDER BY c.ordinal_position
		`).Bind(map[string]any{"tableName": "test_table"}).All(&info)
		if err != nil {
			t.Fatalf("query failed: %v", err)
		}
		if len(info) != 6 {
			t.Fatalf("expected 6 columns, got %d", len(info))
		}

		// Verify the id column
		idCol := info[0]
		if idCol.Name != "id" {
			t.Errorf("expected first column 'id', got %q", idCol.Name)
		}
		if idCol.PK != 1 {
			t.Errorf("expected 'id' to be PK (1), got %d", idCol.PK)
		}
		if !idCol.NotNull {
			t.Error("expected 'id' to be NOT NULL")
		}

		// Verify the name column
		nameCol := info[1]
		if nameCol.Name != "name" {
			t.Errorf("expected second column 'name', got %q", nameCol.Name)
		}
		if nameCol.PK != 0 {
			t.Errorf("expected 'name' to not be PK, got %d", nameCol.PK)
		}
	})

	// Test pg_indexes query (mirrors TableIndexes)
	t.Run("TableIndexes query", func(t *testing.T) {
		type idxRow struct {
			Name string `db:"indexname"`
			Sql  string `db:"indexdef"`
		}
		var indexes []idxRow
		err := db.NewQuery(`
			SELECT indexname, indexdef
			FROM pg_indexes
			WHERE tablename = {:tableName}
			AND indexname NOT IN (
				SELECT conname
				FROM pg_constraint
				WHERE contype = 'p' AND conrelid = {:tableName}::regclass
			)
		`).Bind(map[string]any{"tableName": "test_table"}).All(&indexes)
		if err != nil {
			t.Fatalf("query failed: %v", err)
		}
		if len(indexes) != 1 {
			t.Fatalf("expected 1 index (excluding PK), got %d", len(indexes))
		}
		if indexes[0].Name != "idx_test_table_email" {
			t.Errorf("expected index name 'idx_test_table_email', got %q", indexes[0].Name)
		}
	})

	// Test HasTable query (mirrors hasTable)
	t.Run("HasTable query", func(t *testing.T) {
		var exists int
		err := db.NewQuery(`
			SELECT 1
			FROM information_schema.tables
			WHERE table_schema = current_schema()
			  AND lower(table_name) = lower({:tableName})
			LIMIT 1
		`).Bind(map[string]any{"tableName": "test_table"}).Row(&exists)
		if err != nil {
			t.Fatalf("query failed: %v", err)
		}
		if exists != 1 {
			t.Error("expected table to exist")
		}

		// Non-existent table
		err = db.NewQuery(`
			SELECT 1
			FROM information_schema.tables
			WHERE table_schema = current_schema()
			  AND lower(table_name) = lower({:tableName})
			LIMIT 1
		`).Bind(map[string]any{"tableName": "nonexistent_table"}).Row(&exists)
		if err == nil {
			t.Error("expected error for nonexistent table")
		}
	})

	// Test DeleteTable with CASCADE
	t.Run("DeleteTable CASCADE", func(t *testing.T) {
		// Create a view that depends on the table
		_, err := db.NewQuery(`CREATE VIEW test_view AS SELECT id, name FROM test_table`).Execute()
		if err != nil {
			t.Fatalf("failed to create view: %v", err)
		}

		// Create another table to test CASCADE doesn't affect unrelated tables
		_, err = db.NewQuery(`CREATE TABLE unrelated_table (id TEXT PRIMARY KEY)`).Execute()
		if err != nil {
			t.Fatalf("failed to create unrelated table: %v", err)
		}

		// Drop with CASCADE should also drop the dependent view
		_, err = db.NewQuery(`DROP TABLE IF EXISTS test_table CASCADE`).Execute()
		if err != nil {
			t.Fatalf("DROP TABLE CASCADE failed: %v", err)
		}

		// Verify the view is gone
		var viewExists int
		viewErr := db.NewQuery(`
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_name = 'test_view'
			LIMIT 1
		`).Row(&viewExists)
		if viewErr == nil {
			t.Error("expected dependent view to be dropped by CASCADE")
		}

		// Verify unrelated table still exists
		var unrelatedExists int
		unrelatedErr := db.NewQuery(`
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_name = 'unrelated_table'
			LIMIT 1
		`).Row(&unrelatedExists)
		if unrelatedErr != nil {
			t.Error("unrelated table should still exist after CASCADE drop")
		}
	})
}

// TestViewDependencyCascade tests that PostgreSQL handles view dependencies
// correctly with CASCADE and information_schema queries.
func TestViewDependencyCascade(t *testing.T) {
	pgURL := getTestPostgresURL(t)
	dbName := "pgpb_test_viewcascade_" + randomSuffix()

	connectFunc := PostgresDBConnect(pgURL)
	db, err := connectFunc(dbName)
	if err != nil {
		t.Fatalf("failed to create test db: %v", err)
	}
	defer func() {
		db.Close()
		dropTestDB(t, pgURL, dbName)
	}()

	// Create base table
	db.NewQuery(`CREATE TABLE base_table (id TEXT PRIMARY KEY, name TEXT NOT NULL)`).Execute()

	// Create dependent view
	db.NewQuery(`CREATE VIEW dependent_view AS SELECT id, name FROM base_table`).Execute()

	// Query dependent views via information_schema
	type viewDep struct {
		ViewName  string `db:"view_name"`
		TableName string `db:"referenced_table_name"`
		ViewDef   string `db:"view_definition"`
	}
	var deps []viewDep
	err = db.NewQuery(`
		SELECT
			u.view_name,
			u.table_name AS referenced_table_name,
			v.view_definition
		FROM information_schema.view_table_usage u
		JOIN information_schema.views v
			ON u.view_schema = v.table_schema AND u.view_name = v.table_name
		WHERE u.table_schema = current_schema()
			AND u.table_name = {:tableName}
		ORDER BY u.view_name
	`).Bind(map[string]any{"tableName": "base_table"}).All(&deps)
	if err != nil {
		t.Fatalf("view dependency query failed: %v", err)
	}

	if len(deps) != 1 {
		t.Fatalf("expected 1 dependent view, got %d", len(deps))
	}
	if deps[0].ViewName != "dependent_view" {
		t.Errorf("expected 'dependent_view', got %q", deps[0].ViewName)
	}
}

// helper to get a raw *sql.DB for cleanup
func getRawDB(t *testing.T, pgURL string, dbName string) *sql.DB {
	t.Helper()
	u, _ := url.Parse(pgURL)
	u.Path = "/" + dbName
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatalf("failed to open raw DB: %v", err)
	}
	return db
}
