package pgpb

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
)

const (
	// elevationCooldown prevents repeated password rotations from rapid
	// requests (redirect loop, DoS). Within this window, the cached token
	// is returned without re-rotating.
	elevationCooldown = 30 * time.Second

	// elevationTokenTTL limits the blast radius of a stolen superuser token.
	// Short-lived: forces re-elevation rather than long-lived admin sessions.
	elevationTokenTTL = 15 * time.Minute

	// elevationCookieName is set after elevation to break the redirect loop.
	// The middleware skips elevation when this cookie is present.
	elevationCookieName = "pgpb_elevated"
)

type cachedElevation struct {
	token     string
	expiresAt time.Time
}

// BindAdminElevation registers middleware that auto-elevates allowed users
// to PocketBase superuser when they visit /_/ (the admin dashboard).
//
// Users whose email appears in the PGPB_ADMIN_EMAILS environment variable
// (comma-separated) are eligible. A mapped superuser account is created
// automatically with a random password that rotates on every elevation.
//
// If PGPB_ADMIN_EMAILS is empty or unset, the feature is disabled and
// the default PocketBase admin login is used.
func BindAdminElevation(app *pocketbase.PocketBase, db *sql.DB) {
	var (
		cacheMu sync.Mutex
		cache   = make(map[string]*cachedElevation)
	)

	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Id: "pgpb_admin_elevation",
		Func: func(e *core.ServeEvent) error {
			if err := ensureAdminMapTable(db); err != nil {
				slog.Warn("pgpb: failed to create admin map table (non-fatal)",
					slog.String("error", err.Error()),
				)
			}

			e.Router.BindFunc(func(re *core.RequestEvent) error {
				path := re.Request.URL.Path
				if path != "/_/" && path != "/_" {
					return re.Next()
				}

				if re.Request.Method != http.MethodGet {
					return re.Next()
				}

				allowlist := parseAdminEmails(os.Getenv("PGPB_ADMIN_EMAILS"))
				if len(allowlist) == 0 {
					return re.Next()
				}

				// Break redirect loop: if we just elevated, fall through to admin UI
				if c, err := re.Request.Cookie(elevationCookieName); err == nil && c.Value == "1" {
					return re.Next()
				}

				token := extractAuthToken(re.Request)
				if token == "" {
					return re.Next()
				}

				record, err := app.FindAuthRecordByToken(token, core.TokenTypeAuth)
				if err != nil {
					return re.Next()
				}

				if record.IsSuperuser() {
					return re.Next()
				}

				if !isAllowed(record.Email(), allowlist) {
					return re.JSON(http.StatusForbidden, map[string]any{
						"error": "Not authorized for admin access",
					})
				}

				return handleElevation(app, db, re, record, &cacheMu, cache)
			})

			return e.Next()
		},
		Priority: 994,
	})
}

func handleElevation(app *pocketbase.PocketBase, db *sql.DB, e *core.RequestEvent, user *core.Record, cacheMu *sync.Mutex, cache map[string]*cachedElevation) error {
	// Check cooldown cache to avoid repeated bcrypt hashing
	cacheMu.Lock()
	if cached, ok := cache[user.Id]; ok && time.Now().Before(cached.expiresAt) {
		token := cached.token
		cacheMu.Unlock()
		return serveElevationResponse(e, token)
	}
	cacheMu.Unlock()

	superuser, token, err := elevateToSuperuser(app, db, user)
	if err != nil {
		slog.Warn("pgpb: admin elevation failed",
			slog.String("user", user.Email()),
			slog.String("error", err.Error()),
		)
		return e.JSON(http.StatusInternalServerError, map[string]any{
			"error": "Admin elevation failed",
		})
	}

	// Cache the token to avoid re-rotation during cooldown
	cacheMu.Lock()
	cache[user.Id] = &cachedElevation{
		token:     token,
		expiresAt: time.Now().Add(elevationCooldown),
	}
	cacheMu.Unlock()

	slog.Info("pgpb: admin elevation",
		slog.String("user", user.Email()),
		slog.String("superuser", superuser.Id),
	)

	return serveElevationResponse(e, token)
}

func serveElevationResponse(e *core.RequestEvent, token string) error {
	// Set a short-lived cookie to break the redirect loop
	http.SetCookie(e.Response, &http.Cookie{
		Name:     elevationCookieName,
		Value:    "1",
		Path:     "/_/",
		MaxAge:   5, // 5 seconds — just enough for the redirect
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   e.Request.TLS != nil,
	})

	// Escape </script> sequences to prevent XSS via field injection.
	// json.Marshal does not HTML-escape by default.
	safeToken := strings.ReplaceAll(token, "</", "<\\/")

	// Serve a minimal HTML page that stores the token and redirects.
	// The token is intentionally placed in the page — this is the same
	// security model as PocketBase's normal auth response (token in JSON body).
	html := fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Redirecting...</title></head><body>
<script>
try{localStorage.setItem("pocketbase_auth",JSON.stringify({token:"%s"}))}catch(e){}
window.location.replace("/_/");
</script>
<noscript><p>JavaScript is required. <a href="/_/">Continue</a></p></noscript>
</body></html>`, safeToken)

	return e.HTML(http.StatusOK, html)
}

func elevateToSuperuser(app *pocketbase.PocketBase, db *sql.DB, user *core.Record) (*core.Record, string, error) {
	mappedEmail := superuserEmail(user.Id)

	var superuserID string
	err := db.QueryRow(
		`SELECT superuser_id FROM pgpb_admin_map WHERE user_id = $1`,
		user.Id,
	).Scan(&superuserID)

	var superuser *core.Record

	if err == sql.ErrNoRows {
		superuser, err = createMappedSuperuser(app, db, user, mappedEmail)
		if err != nil {
			return nil, "", err
		}
	} else if err != nil {
		return nil, "", fmt.Errorf("query admin map: %w", err)
	} else {
		superuser, err = app.FindRecordById(core.CollectionNameSuperusers, superuserID)
		if err != nil {
			// Stale mapping — superuser was deleted. Remove mapping and re-create.
			db.Exec(`DELETE FROM pgpb_admin_map WHERE user_id = $1`, user.Id)
			superuser, err = createMappedSuperuser(app, db, user, mappedEmail)
			if err != nil {
				return nil, "", err
			}
		} else {
			superuser.SetRandomPassword()
			if saveErr := app.Save(superuser); saveErr != nil {
				return nil, "", fmt.Errorf("rotate superuser password: %w", saveErr)
			}

			db.Exec(
				`UPDATE pgpb_admin_map SET last_elevated_at = NOW(), user_email = $1 WHERE user_id = $2`,
				user.Email(), user.Id,
			)
		}
	}

	token, err := superuser.NewStaticAuthToken(elevationTokenTTL)
	if err != nil {
		return nil, "", fmt.Errorf("generate auth token: %w", err)
	}

	return superuser, token, nil
}

func createMappedSuperuser(app *pocketbase.PocketBase, db *sql.DB, user *core.Record, mappedEmail string) (*core.Record, error) {
	collection, err := app.FindCollectionByNameOrId(core.CollectionNameSuperusers)
	if err != nil {
		return nil, fmt.Errorf("find superusers collection: %w", err)
	}

	superuser := core.NewRecord(collection)
	superuser.SetEmail(mappedEmail)
	superuser.SetVerified(true)
	superuser.SetRandomPassword()

	if err := app.Save(superuser); err != nil {
		return nil, fmt.Errorf("create superuser: %w", err)
	}

	// Use INSERT ON CONFLICT to handle race conditions — if two requests
	// try to create a mapping simultaneously, the second one is a no-op.
	_, err = db.Exec(
		`INSERT INTO pgpb_admin_map (user_id, superuser_id, user_email)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (user_id) DO NOTHING`,
		user.Id, superuser.Id, user.Email(),
	)
	if err != nil {
		return nil, fmt.Errorf("insert admin map: %w", err)
	}

	return superuser, nil
}

func ensureAdminMapTable(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS pgpb_admin_map (
			user_id          TEXT PRIMARY KEY,
			superuser_id     TEXT NOT NULL,
			user_email       TEXT NOT NULL,
			last_elevated_at TIMESTAMPTZ DEFAULT NOW(),
			created_at       TIMESTAMPTZ DEFAULT NOW()
		)
	`)
	return err
}

func parseAdminEmails(envVal string) []string {
	if strings.TrimSpace(envVal) == "" {
		return nil
	}

	parts := strings.Split(envVal, ",")
	var result []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(strings.ToLower(p))
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func isAllowed(email string, allowlist []string) bool {
	if email == "" || len(allowlist) == 0 {
		return false
	}
	lower := strings.ToLower(email)
	for _, a := range allowlist {
		if a == lower {
			return true
		}
	}
	return false
}

func superuserEmail(userID string) string {
	return "pgpb_" + userID + "@internal"
}

func extractAuthToken(r *http.Request) string {
	if auth := r.Header.Get("Authorization"); auth != "" {
		token := auth
		if strings.HasPrefix(strings.ToLower(token), "bearer ") {
			token = token[7:]
		}
		return strings.TrimSpace(token)
	}

	for _, name := range []string{"pb_auth"} {
		if c, err := r.Cookie(name); err == nil && c.Value != "" {
			val := c.Value
			if strings.HasPrefix(val, "{") {
				var data struct {
					Token string `json:"token"`
				}
				if json.Unmarshal([]byte(val), &data) == nil && data.Token != "" {
					return data.Token
				}
			}
			return val
		}
	}

	return ""
}
