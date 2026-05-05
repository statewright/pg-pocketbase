package pgpb

import (
	"testing"
)

func TestParseS3ConfigFromEnv(t *testing.T) {
	t.Setenv("PB_S3_ENABLED", "true")
	t.Setenv("PB_S3_ENDPOINT", "http://trove:9000")
	t.Setenv("PB_S3_BUCKET", "pb-files")
	t.Setenv("PB_S3_REGION", "us-east-1")
	t.Setenv("PB_S3_ACCESS_KEY", "AKID")
	t.Setenv("PB_S3_SECRET", "secret")
	t.Setenv("PB_S3_FORCE_PATH_STYLE", "true")

	cfg := parseS3EnvConfig("PB_S3_")
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if !cfg.Enabled {
		t.Fatal("expected enabled")
	}
	if cfg.Endpoint != "http://trove:9000" {
		t.Fatalf("expected endpoint http://trove:9000, got %s", cfg.Endpoint)
	}
	if cfg.Bucket != "pb-files" {
		t.Fatalf("expected bucket pb-files, got %s", cfg.Bucket)
	}
	if cfg.Region != "us-east-1" {
		t.Fatalf("expected region us-east-1, got %s", cfg.Region)
	}
	if cfg.AccessKey != "AKID" {
		t.Fatalf("expected access key AKID, got %s", cfg.AccessKey)
	}
	if cfg.Secret != "secret" {
		t.Fatalf("expected secret, got %s", cfg.Secret)
	}
	if !cfg.ForcePathStyle {
		t.Fatal("expected force path style")
	}
}

func TestParseS3ConfigFromEnv_Disabled(t *testing.T) {
	// No PB_S3_ENABLED set
	cfg := parseS3EnvConfig("PB_S3_")
	if cfg != nil {
		t.Fatal("expected nil config when not enabled")
	}
}

func TestParseS3ConfigFromEnv_BackupsPrefix(t *testing.T) {
	t.Setenv("PB_BACKUPS_S3_ENABLED", "true")
	t.Setenv("PB_BACKUPS_S3_ENDPOINT", "http://trove:9000")
	t.Setenv("PB_BACKUPS_S3_BUCKET", "pb-backups")
	t.Setenv("PB_BACKUPS_S3_REGION", "us-east-1")
	t.Setenv("PB_BACKUPS_S3_ACCESS_KEY", "AKID")
	t.Setenv("PB_BACKUPS_S3_SECRET", "secret")
	t.Setenv("PB_BACKUPS_S3_FORCE_PATH_STYLE", "true")

	cfg := parseS3EnvConfig("PB_BACKUPS_S3_")
	if cfg == nil {
		t.Fatal("expected non-nil config for backups prefix")
	}
	if cfg.Bucket != "pb-backups" {
		t.Fatalf("expected bucket pb-backups, got %s", cfg.Bucket)
	}
}
