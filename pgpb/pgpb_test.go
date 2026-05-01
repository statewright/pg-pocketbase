package pgpb

import (
	"testing"
)

func TestNewWithPostgres_Creates(t *testing.T) {
	pgURL := getTestPostgresURL(t)

	app := NewWithPostgres(pgURL,
		WithDataDBName("pgpb_test_new_data_"+randomSuffix()),
		WithAuxDBName("pgpb_test_new_aux_"+randomSuffix()),
	)

	if app == nil {
		t.Fatal("NewWithPostgres returned nil")
	}

	// Verify the app was created (not bootstrapped yet -- that requires
	// the full PB migration stack which needs the schema translation layer)
	if app.RootCmd == nil {
		t.Fatal("expected RootCmd to be set")
	}
}

func TestNewWithPostgres_DefaultDBNames(t *testing.T) {
	pgURL := getTestPostgresURL(t)

	cfg := defaultPgConfig()
	if cfg.dataDBName != "pb_data" {
		t.Errorf("expected default data DB name 'pb_data', got %q", cfg.dataDBName)
	}
	if cfg.auxDBName != "pb_auxiliary" {
		t.Errorf("expected default aux DB name 'pb_auxiliary', got %q", cfg.auxDBName)
	}

	_ = pgURL // used only to verify test environment
}

func TestRouteDBConnect_RoutesCorrectly(t *testing.T) {
	pgURL := getTestPostgresURL(t)

	dataSuffix := randomSuffix()
	auxSuffix := randomSuffix()
	dataName := "pgpb_route_data_" + dataSuffix
	auxName := "pgpb_route_aux_" + auxSuffix

	cfg := pgConfig{
		dataMaxOpenConns: 10,
		dataMaxIdleConns: 2,
		auxMaxOpenConns:  5,
		auxMaxIdleConns:  1,
		dataDBName:       dataName,
		auxDBName:        auxName,
	}

	connectFunc := routeDBConnect(pgURL, cfg)

	// Test data routing
	dataDB, err := connectFunc("/some/path/data.db")
	if err != nil {
		t.Fatalf("data route failed: %v", err)
	}
	defer func() {
		dataDB.Close()
		dropTestDB(t, pgURL, dataName)
	}()

	if err := dataDB.DB().Ping(); err != nil {
		t.Fatalf("data DB ping failed: %v", err)
	}

	// Test auxiliary routing
	auxDB, err := connectFunc("/some/path/auxiliary.db")
	if err != nil {
		t.Fatalf("aux route failed: %v", err)
	}
	defer func() {
		auxDB.Close()
		dropTestDB(t, pgURL, auxName)
	}()

	if err := auxDB.DB().Ping(); err != nil {
		t.Fatalf("aux DB ping failed: %v", err)
	}
}
