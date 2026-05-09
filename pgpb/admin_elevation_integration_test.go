package pgpb

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pocketbase/pocketbase/core"
)

func newTestRequest(method, path string) (*http.Request, *httptest.ResponseRecorder) {
	return httptest.NewRequest(method, path, nil), httptest.NewRecorder()
}

func testElevationSetup(t *testing.T) (app core.App, elevDB *sql.DB, usersCol *core.Collection, cleanup func()) {
	t.Helper()

	pgURL := getTestPostgresURL(t)
	suffix := randomSuffix()
	dataDB := "pgpb_elev_" + t.Name() + "_" + suffix
	auxDB := "pgpb_elev_aux_" + t.Name() + "_" + suffix

	pbApp := NewWithPostgres(pgURL,
		WithDataDBName(dataDB),
		WithAuxDBName(auxDB),
	)

	if err := pbApp.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap failed: %v", err)
	}

	u := mustParseConnURL(pgURL)
	u.Path = "/" + dataDB
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatalf("failed to open elev db: %v", err)
	}

	if err := ensureAdminMapTable(db); err != nil {
		t.Fatalf("failed to create admin map table: %v", err)
	}

	col := core.NewBaseCollection("test_users")
	col.Type = core.CollectionTypeAuth
	if err := pbApp.SaveNoValidate(col); err != nil {
		t.Fatalf("failed to create test_users collection: %v", err)
	}

	cleanup = func() {
		db.Close()
		dropTestDB(t, pgURL, dataDB)
		dropTestDB(t, pgURL, auxDB)
	}

	return pbApp, db, col, cleanup
}

func createTestUser(t *testing.T, app core.App, col *core.Collection, email string) *core.Record {
	t.Helper()
	user := core.NewRecord(col)
	user.SetEmail(email)
	user.SetPassword("testpass123456")
	user.SetVerified(true)
	if err := app.Save(user); err != nil {
		t.Fatalf("failed to create test user %s: %v", email, err)
	}
	return user
}

func TestElevation_CreatesMapping(t *testing.T) {
	app, db, col, cleanup := testElevationSetup(t)
	defer cleanup()

	user := createTestUser(t, app, col, "admin@test.com")

	superuser, token, err := elevateToSuperuser(app, db, user)
	if err != nil {
		t.Fatalf("elevation failed: %v", err)
	}

	// Verify superuser was created
	if superuser.Id == "" {
		t.Fatal("superuser ID should not be empty")
	}
	expectedEmail := "pgpb_" + user.Id + "@internal.localhost"
	if superuser.Email() != expectedEmail {
		t.Fatalf("expected email %q, got %q", expectedEmail, superuser.Email())
	}
	if !superuser.Verified() {
		t.Fatal("superuser should be verified")
	}

	// Verify token is valid
	found, err := app.FindAuthRecordByToken(token, core.TokenTypeAuth)
	if err != nil {
		t.Fatalf("token should be valid: %v", err)
	}
	if found.Id != superuser.Id {
		t.Fatalf("token should resolve to the mapped superuser")
	}

	// Verify mapping exists in DB
	var mappedID string
	err = db.QueryRow(
		`SELECT superuser_id FROM pgpb_admin_map WHERE user_id = $1`,
		user.Id,
	).Scan(&mappedID)
	if err != nil {
		t.Fatalf("mapping not found: %v", err)
	}
	if mappedID != superuser.Id {
		t.Fatalf("mapping points to wrong superuser: %s vs %s", mappedID, superuser.Id)
	}
}

func TestElevation_PasswordRotation(t *testing.T) {
	app, db, col, cleanup := testElevationSetup(t)
	defer cleanup()

	user := createTestUser(t, app, col, "admin@test.com")

	superuser1, token1, err := elevateToSuperuser(app, db, user)
	if err != nil {
		t.Fatalf("first elevation failed: %v", err)
	}

	tokenKey1 := superuser1.TokenKey()

	superuser2, token2, err := elevateToSuperuser(app, db, user)
	if err != nil {
		t.Fatalf("second elevation failed: %v", err)
	}

	// Same superuser ID
	if superuser1.Id != superuser2.Id {
		t.Fatalf("expected same superuser ID: %s vs %s", superuser1.Id, superuser2.Id)
	}

	// Token key should have rotated
	tokenKey2 := superuser2.TokenKey()
	if tokenKey1 == tokenKey2 {
		t.Fatal("token key should rotate on re-elevation")
	}

	// Different tokens
	if token1 == token2 {
		t.Fatal("tokens should differ after rotation")
	}

	// Old token invalid
	_, err = app.FindAuthRecordByToken(token1, core.TokenTypeAuth)
	if err == nil {
		t.Fatal("old token should be invalid after rotation")
	}

	// New token valid
	_, err = app.FindAuthRecordByToken(token2, core.TokenTypeAuth)
	if err != nil {
		t.Fatalf("new token should be valid: %v", err)
	}
}

func TestElevation_StaleMapping(t *testing.T) {
	app, db, col, cleanup := testElevationSetup(t)
	defer cleanup()

	user := createTestUser(t, app, col, "admin@test.com")

	superuser1, _, err := elevateToSuperuser(app, db, user)
	if err != nil {
		t.Fatalf("first elevation failed: %v", err)
	}

	// PB prevents deleting the last superuser, so create a spare first
	spareSuperuser := core.NewRecord(superuser1.Collection())
	spareSuperuser.SetEmail("spare@internal.localhost")
	spareSuperuser.SetRandomPassword()
	spareSuperuser.SetVerified(true)
	if err := app.Save(spareSuperuser); err != nil {
		t.Fatalf("failed to create spare superuser: %v", err)
	}

	// Delete the mapped superuser to simulate admin deletion
	if err := app.Delete(superuser1); err != nil {
		t.Fatalf("failed to delete superuser: %v", err)
	}

	// Re-elevation should recover
	superuser2, token2, err := elevateToSuperuser(app, db, user)
	if err != nil {
		t.Fatalf("re-elevation should succeed after stale mapping: %v", err)
	}

	if superuser2.Id == superuser1.Id {
		t.Fatal("should create a NEW superuser after stale recovery")
	}

	_, err = app.FindAuthRecordByToken(token2, core.TokenTypeAuth)
	if err != nil {
		t.Fatalf("new token should be valid: %v", err)
	}
}

func TestElevation_MappingPersistence(t *testing.T) {
	app, db, col, cleanup := testElevationSetup(t)
	defer cleanup()

	user := createTestUser(t, app, col, "admin@test.com")

	s1, _, _ := elevateToSuperuser(app, db, user)
	s2, _, _ := elevateToSuperuser(app, db, user)
	s3, _, _ := elevateToSuperuser(app, db, user)

	if s1.Id != s2.Id || s2.Id != s3.Id {
		t.Fatal("same user should always map to the same superuser")
	}
}

func TestElevation_MultipleUsers(t *testing.T) {
	app, db, col, cleanup := testElevationSetup(t)
	defer cleanup()

	user1 := createTestUser(t, app, col, "alice@test.com")
	user2 := createTestUser(t, app, col, "bob@test.com")

	s1, _, err := elevateToSuperuser(app, db, user1)
	if err != nil {
		t.Fatalf("elevation for user1 failed: %v", err)
	}

	s2, _, err := elevateToSuperuser(app, db, user2)
	if err != nil {
		t.Fatalf("elevation for user2 failed: %v", err)
	}

	if s1.Id == s2.Id {
		t.Fatal("different users should map to different superusers")
	}

	// Verify both mappings
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM pgpb_admin_map`).Scan(&count)
	if count != 2 {
		t.Fatalf("expected 2 mappings, got %d", count)
	}
}

func TestElevation_ExtractAuthToken_Header(t *testing.T) {
	req, _ := newTestRequest("GET", "/_/")
	req.Header.Set("Authorization", "Bearer mytoken123")

	token := extractAuthToken(req)
	if token != "mytoken123" {
		t.Fatalf("expected mytoken123, got %q", token)
	}
}

func TestElevation_ExtractAuthToken_BearerCaseInsensitive(t *testing.T) {
	req, _ := newTestRequest("GET", "/_/")
	req.Header.Set("Authorization", "bearer mytoken123")

	token := extractAuthToken(req)
	if token != "mytoken123" {
		t.Fatalf("expected mytoken123, got %q", token)
	}
}

func TestElevation_ExtractAuthToken_NoBearerPrefix(t *testing.T) {
	req, _ := newTestRequest("GET", "/_/")
	req.Header.Set("Authorization", "rawtoken456")

	token := extractAuthToken(req)
	if token != "rawtoken456" {
		t.Fatalf("expected rawtoken456, got %q", token)
	}
}

func TestElevation_ExtractAuthToken_Cookie(t *testing.T) {
	req, _ := newTestRequest("GET", "/_/")
	req.AddCookie(&http.Cookie{Name: "pb_auth", Value: "cookietoken789"})

	token := extractAuthToken(req)
	if token != "cookietoken789" {
		t.Fatalf("expected cookietoken789, got %q", token)
	}
}

func TestElevation_ExtractAuthToken_CookieJSON(t *testing.T) {
	// In real browsers, the PB JS SDK URL-encodes the cookie value.
	// Go's cookie parser is strict about special characters, so we
	// URL-encode the JSON as the real client would.
	req, _ := newTestRequest("GET", "/_/")
	// URL-encoded: {"token":"jsontoken999","record":{}}
	encoded := "%7B%22token%22%3A%22jsontoken999%22%2C%22record%22%3A%7B%7D%7D"
	req.AddCookie(&http.Cookie{Name: "pb_auth", Value: encoded})

	token := extractAuthToken(req)
	if token != "jsontoken999" {
		t.Fatalf("expected jsontoken999, got %q", token)
	}
}

func TestElevation_ExtractAuthToken_Empty(t *testing.T) {
	req, _ := newTestRequest("GET", "/_/")

	token := extractAuthToken(req)
	if token != "" {
		t.Fatalf("expected empty, got %q", token)
	}
}

func TestElevation_ExtractAuthToken_HeaderTakesPrecedence(t *testing.T) {
	req, _ := newTestRequest("GET", "/_/")
	req.Header.Set("Authorization", "Bearer headertoken")
	req.AddCookie(&http.Cookie{Name: "pb_auth", Value: "cookietoken"})

	token := extractAuthToken(req)
	if token != "headertoken" {
		t.Fatalf("expected headertoken (header should take precedence), got %q", token)
	}
}
