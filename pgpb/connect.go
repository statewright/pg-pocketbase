package pgpb

import (
	"database/sql"
	"fmt"
	"net/url"
	"regexp"
	"time"

	"github.com/pocketbase/dbx"
	_ "github.com/jackc/pgx/v5/stdlib"
)

var reDBNotExist = regexp.MustCompile(`database ".+" does not exist`)

// ConnectOption configures the PostgreSQL connection.
type ConnectOption func(*connectConfig)

type connectConfig struct {
	maxOpenConns int
	maxIdleConns int
	maxIdleTime  time.Duration
}

func defaultConnectConfig() connectConfig {
	return connectConfig{
		maxOpenConns: 70,
		maxIdleConns: 15,
		maxIdleTime:  3 * time.Minute,
	}
}

// WithMaxOpenConns sets the maximum number of open connections.
func WithMaxOpenConns(n int) ConnectOption {
	return func(c *connectConfig) { c.maxOpenConns = n }
}

// WithMaxIdleConns sets the maximum number of idle connections.
func WithMaxIdleConns(n int) ConnectOption {
	return func(c *connectConfig) { c.maxIdleConns = n }
}

// PostgresDBConnect returns a DBConnectFunc that connects to PostgreSQL.
// The connectionString should be a postgres:// or postgresql:// URL.
// The returned function accepts a database name and returns a configured *dbx.DB.
func PostgresDBConnect(connectionString string, opts ...ConnectOption) func(dbName string) (*dbx.DB, error) {
	u, err := url.Parse(connectionString)
	if err != nil {
		panic(fmt.Sprintf("pgpb: invalid connection string: %v", err))
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		panic(fmt.Sprintf("pgpb: unsupported scheme %q, expected postgres:// or postgresql://", u.Scheme))
	}

	cfg := defaultConnectConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	return func(dbName string) (*dbx.DB, error) {
		connURL := *u
		connURL.Path = "/" + dbName

		db, openErr := dbx.Open("pgx", connURL.String())
		if openErr != nil {
			return nil, fmt.Errorf("pgpb: failed to open database %q: %w", dbName, openErr)
		}

		// Test the connection -- if DB doesn't exist, create it
		if pingErr := db.DB().Ping(); pingErr != nil {
			db.Close()
			if reDBNotExist.MatchString(pingErr.Error()) {
				if createErr := createDatabase(u, dbName); createErr != nil {
					return nil, fmt.Errorf("pgpb: failed to auto-create database %q: %w", dbName, createErr)
				}
				// Retry after creation
				db, openErr = dbx.Open("pgx", connURL.String())
				if openErr != nil {
					return nil, fmt.Errorf("pgpb: failed to open database %q after creation: %w", dbName, openErr)
				}
			} else {
				return nil, fmt.Errorf("pgpb: failed to connect to database %q: %w", dbName, pingErr)
			}
		}

		db.DB().SetMaxOpenConns(cfg.maxOpenConns)
		db.DB().SetMaxIdleConns(cfg.maxIdleConns)
		db.DB().SetConnMaxIdleTime(cfg.maxIdleTime)

		return db, nil
	}
}

// createDatabase creates a new PostgreSQL database using the admin connection.
func createDatabase(baseURL *url.URL, dbName string) error {
	adminURL := *baseURL
	adminURL.Path = "/postgres"

	adminDB, err := sql.Open("pgx", adminURL.String())
	if err != nil {
		return fmt.Errorf("failed to connect to admin database: %w", err)
	}
	defer adminDB.Close()

	// CREATE DATABASE cannot use prepared statements
	_, err = adminDB.Exec(fmt.Sprintf(`CREATE DATABASE "%s"`, dbName))
	if err != nil {
		return fmt.Errorf("failed to create database %q: %w", dbName, err)
	}

	return nil
}
