package pgpb

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
)

// InitSentry initializes the Sentry SDK for error reporting.
//
// Reads SENTRY_DSN and SENTRY_ENVIRONMENT from environment variables.
// If SENTRY_DSN is empty, this is a no-op.
//
// The SDK captures:
//   - Go panics in HTTP handlers (patched into panicRecover middleware)
//   - Goja JS VM errors (patched into normalizeException in jsvm plugin)
//   - Errors reported via CaptureGojaError / CaptureError helpers
//
// Call this after NewWithPostgres() and before app.Start():
//
//	app := pgpb.NewWithPostgres(connString)
//	pgpb.InitSentry(app)
//	app.Start()
func InitSentry(app *pocketbase.PocketBase) {
	dsn := os.Getenv("SENTRY_DSN")
	if dsn == "" {
		return
	}

	env := os.Getenv("SENTRY_ENVIRONMENT")
	if env == "" {
		env = "production"
	}

	err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Environment:      env,
		Release:          "pg-pocketbase@" + pocketbase.Version,
		TracesSampleRate: 0.1,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pgpb: sentry init failed: %v\n", err)
		return
	}

	log.Println("pgpb: sentry initialized (env=" + env + ")")

	// Flush on shutdown
	app.OnTerminate().Bind(&hook.Handler[*core.TerminateEvent]{
		Id: "pgpb_sentry_flush",
		Func: func(e *core.TerminateEvent) error {
			sentry.Flush(2 * time.Second)
			return e.Next()
		},
	})
}
