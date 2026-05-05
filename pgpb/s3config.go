package pgpb

import (
	"log/slog"
	"os"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
)

// s3EnvConfig holds S3 configuration parsed from environment variables.
type s3EnvConfig struct {
	Enabled        bool
	Endpoint       string
	Bucket         string
	Region         string
	AccessKey      string
	Secret         string
	ForcePathStyle bool
}

// parseS3EnvConfig reads S3 configuration from environment variables with the given prefix.
// Returns nil if not enabled.
func parseS3EnvConfig(prefix string) *s3EnvConfig {
	enabled := os.Getenv(prefix + "ENABLED")
	if enabled != "true" && enabled != "1" {
		return nil
	}

	return &s3EnvConfig{
		Enabled:        true,
		Endpoint:       os.Getenv(prefix + "ENDPOINT"),
		Bucket:         os.Getenv(prefix + "BUCKET"),
		Region:         os.Getenv(prefix + "REGION"),
		AccessKey:      os.Getenv(prefix + "ACCESS_KEY"),
		Secret:         os.Getenv(prefix + "SECRET"),
		ForcePathStyle: os.Getenv(prefix+"FORCE_PATH_STYLE") == "true" || os.Getenv(prefix+"FORCE_PATH_STYLE") == "1",
	}
}

// BindS3AutoConfig registers hooks that apply S3 configuration from environment
// variables on startup. This allows S3 settings to be configured entirely via
// env vars (container-friendly) without requiring admin API calls after deploy.
//
// Environment variables (prefix PB_S3_ for storage, PB_BACKUPS_S3_ for backups):
//
//	PB_S3_ENABLED=true
//	PB_S3_ENDPOINT=http://trove:9000
//	PB_S3_BUCKET=pb-files
//	PB_S3_REGION=us-east-1
//	PB_S3_ACCESS_KEY=your-access-key
//	PB_S3_SECRET=your-secret-key
//	PB_S3_FORCE_PATH_STYLE=true
func BindS3AutoConfig(app *pocketbase.PocketBase) {
	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Id: "pgpb_s3_autoconfig",
		Func: func(e *core.ServeEvent) error {
			changed := false

			if cfg := parseS3EnvConfig("PB_S3_"); cfg != nil {
				s := app.Settings()
				s.S3.Enabled = cfg.Enabled
				s.S3.Endpoint = cfg.Endpoint
				s.S3.Bucket = cfg.Bucket
				s.S3.Region = cfg.Region
				s.S3.AccessKey = cfg.AccessKey
				s.S3.Secret = cfg.Secret
				s.S3.ForcePathStyle = cfg.ForcePathStyle
				changed = true
				slog.Info("pgpb: S3 storage configured from environment",
					slog.String("endpoint", cfg.Endpoint),
					slog.String("bucket", cfg.Bucket),
				)
			}

			if cfg := parseS3EnvConfig("PB_BACKUPS_S3_"); cfg != nil {
				s := app.Settings()
				s.Backups.S3.Enabled = cfg.Enabled
				s.Backups.S3.Endpoint = cfg.Endpoint
				s.Backups.S3.Bucket = cfg.Bucket
				s.Backups.S3.Region = cfg.Region
				s.Backups.S3.AccessKey = cfg.AccessKey
				s.Backups.S3.Secret = cfg.Secret
				s.Backups.S3.ForcePathStyle = cfg.ForcePathStyle
				changed = true
				slog.Info("pgpb: S3 backups configured from environment",
					slog.String("endpoint", cfg.Endpoint),
					slog.String("bucket", cfg.Bucket),
				)
			}

			if changed {
				if err := app.Save(app.Settings()); err != nil {
					slog.Warn("pgpb: failed to save S3 settings", slog.String("error", err.Error()))
				}
			}

			return e.Next()
		},
		Priority: 997, // after bootstrap, before bridge
	})
}
