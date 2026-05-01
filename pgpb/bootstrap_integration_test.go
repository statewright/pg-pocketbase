package pgpb

import (
	"testing"
)

func TestBootstrapAgainstPostgres(t *testing.T) {
	pgURL := getTestPostgresURL(t)

	dataSuffix := randomSuffix()
	dataDB := "pgpb_boot_data_" + dataSuffix
	auxDB := "pgpb_boot_aux_" + dataSuffix

	app := NewWithPostgres(pgURL,
		WithDataDBName(dataDB),
		WithAuxDBName(auxDB),
	)

	defer func() {
		dropTestDB(t, pgURL, dataDB)
		dropTestDB(t, pgURL, auxDB)
	}()

	// This runs the full migration stack: creates system tables,
	// system collections, indexes, etc.
	if err := app.Bootstrap(); err != nil {
		t.Logf("Full error chain: %+v", err)
		t.Fatalf("Bootstrap failed: %v", err)
	}

	// Verify system collections exist
	collections := []string{
		"_superusers",
		"_authOrigins",
		"_externalAuths",
		"_mfas",
		"_otps",
	}

	for _, name := range collections {
		col, err := app.FindCollectionByNameOrId(name)
		if err != nil {
			t.Errorf("system collection %q not found: %v", name, err)
			continue
		}
		if col.Name != name {
			t.Errorf("expected collection name %q, got %q", name, col.Name)
		}
	}

	t.Logf("Bootstrap succeeded with %d system collections verified", len(collections))
}
